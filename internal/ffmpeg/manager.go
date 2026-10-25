package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// CheckResult describes the executable and video encoder selected for capture.
type CheckResult struct {
	Path    string
	Encoder string
}

// Manager owns at most one FFmpeg capture process. It has no package-level
// state, so a server can be started and stopped independently in tests or apps.
type Manager struct {
	binary string
	logger *log.Logger

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}

	checkMu sync.Mutex
	checked *CheckResult
}

func NewManager(binary string, logger *log.Logger) *Manager {
	return &Manager{binary: binary, logger: logger}
}

// Check verifies that FFmpeg is available and selects NVENC when the encoder
// is advertised. libx264 is the CPU fallback. The probe takes a noticeable
// moment, so a successful result is cached for later sessions.
func (m *Manager) Check(ctx context.Context) (CheckResult, error) {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	if m.checked != nil {
		return *m.checked, nil
	}
	result, err := m.probe(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	m.checked = &result
	return result, nil
}

func (m *Manager) probe(ctx context.Context) (CheckResult, error) {
	if runtime.GOOS != "windows" {
		return CheckResult{}, fmt.Errorf("desktop capture is implemented for Windows only")
	}

	path, err := exec.LookPath(m.binary)
	if err != nil {
		return CheckResult{}, fmt.Errorf("find FFmpeg executable %q: %w", m.binary, err)
	}

	command := exec.CommandContext(ctx, path, "-hide_banner", "-encoders")
	output, err := command.CombinedOutput()
	if err != nil {
		return CheckResult{}, fmt.Errorf("inspect FFmpeg encoders: %w: %s", err, strings.TrimSpace(string(output)))
	}

	encoders := string(output)
	if strings.Contains(encoders, "h264_nvenc") && encoderWorks(ctx, path, "h264_nvenc") {
		return CheckResult{Path: path, Encoder: "h264_nvenc"}, nil
	}
	if strings.Contains(encoders, "h264_nvenc") {
		m.logger.Printf("FFmpeg advertises h264_nvenc but it is unavailable at runtime; falling back to libx264")
	}
	if strings.Contains(encoders, "libx264") && encoderWorks(ctx, path, "libx264") {
		return CheckResult{Path: path, Encoder: "libx264"}, nil
	}

	return CheckResult{}, errors.New("FFmpeg has neither h264_nvenc nor libx264")
}

func encoderWorks(ctx context.Context, path, encoder string) bool {
	command := exec.CommandContext(ctx, path,
		"-hide_banner",
		"-loglevel", "error",
		"-f", "lavfi",
		// NVENC rejects frames smaller than about 145x49, so a tiny probe
		// frame would always report it as unavailable.
		"-i", "color=c=black:s=256x256:r=1",
		"-frames:v", "1",
		"-c:v", encoder,
		"-f", "null",
		"-",
	)
	return command.Run() == nil
}

// Start begins a gdigrab desktop capture that sends H.264 RTP to localhost.
// The process is tied to parentContext and is stopped by either its cancellation
// or Stop.
func (m *Manager) Start(parentContext context.Context, rtpPort int, profile Profile) error {
	if rtpPort < 1 || rtpPort > 65535 {
		return fmt.Errorf("invalid RTP port %d", rtpPort)
	}

	check, err := m.Check(parentContext)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil {
		return errors.New("FFmpeg capture is already running")
	}

	processContext, cancel := context.WithCancel(parentContext)
	command := exec.CommandContext(processContext, check.Path, captureArguments(check.Encoder, rtpPort, profile)...)
	command.Stdout = m.logger.Writer()
	command.Stderr = m.logger.Writer()
	if err := command.Start(); err != nil {
		cancel()
		return fmt.Errorf("start FFmpeg with %s: %w", check.Encoder, err)
	}
	if err := bindToHostLifetime(command.Process); err != nil {
		m.logger.Printf("FFmpeg may outlive the host if it crashes: %v", err)
	}

	done := make(chan struct{})
	m.cmd = command
	m.cancel = cancel
	m.done = done
	m.logger.Printf("FFmpeg started: encoder=%s profile=%s (%d FPS, %s, max width %d) RTP=127.0.0.1:%d",
		check.Encoder, profile.ID, profile.FPS, profile.bitrate(), profile.MaxWidth, rtpPort)

	go m.wait(command, processContext, done)
	return nil
}

// Stop terminates the active capture and waits for FFmpeg to exit.
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()

	if cancel == nil || done == nil {
		return
	}
	cancel()
	<-done
}

func (m *Manager) wait(command *exec.Cmd, processContext context.Context, done chan struct{}) {
	err := command.Wait()

	m.mu.Lock()
	if m.cmd == command {
		m.cmd = nil
		m.cancel = nil
		m.done = nil
	}
	m.mu.Unlock()

	close(done)
	if processContext.Err() != nil {
		m.logger.Printf("FFmpeg capture stopped")
		return
	}
	if err != nil {
		m.logger.Printf("FFmpeg capture exited unexpectedly: %v", err)
		return
	}
	m.logger.Printf("FFmpeg capture exited")
}

func captureArguments(encoder string, rtpPort int, profile Profile) []string {
	arguments := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-f", "gdigrab",
		"-framerate", strconv.Itoa(profile.FPS),
		"-draw_mouse", "1",
		"-i", "desktop",
		"-an",
		"-vf", profile.videoFilter(),
		"-c:v", encoder,
		"-profile:v", "baseline",
		"-g", strconv.Itoa(profile.gop()),
		"-keyint_min", strconv.Itoa(profile.gop()),
		"-sc_threshold", "0",
		"-bf", "0",
		"-max_delay", "0",
		"-b:v", profile.bitrate(),
		"-maxrate", profile.bitrate(),
		"-bufsize", profile.bufferSize(),
	}

	if encoder == "h264_nvenc" {
		arguments = append(arguments,
			"-preset", profile.nvencPreset,
			"-tune", "ll",
			"-rc", "cbr",
			"-zerolatency", "1",
			"-forced-idr", "1",
		)
	} else {
		gop := strconv.Itoa(profile.gop())
		arguments = append(arguments,
			"-preset", profile.x264Preset,
			"-tune", "zerolatency",
			"-x264-params", "repeat-headers=1:keyint="+gop+":min-keyint="+gop+":scenecut=0",
		)
	}

	return append(arguments,
		"-flush_packets", "1",
		"-payload_type", "96",
		"-f", "rtp",
		// Binding the sender to loopback keeps the stream off the network and
		// avoids a Windows Firewall prompt for ffmpeg.exe.
		fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200&localaddr=127.0.0.1", rtpPort),
	)
}
