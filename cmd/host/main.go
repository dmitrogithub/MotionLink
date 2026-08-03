package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"motionlink/internal/ffmpeg"
	"motionlink/internal/rtc"
	"motionlink/internal/signaling"
)

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:8080", "HTTP signaling address")
	rtpPort := flag.Int("rtp-port", 5004, "local UDP port that receives H.264 RTP from FFmpeg")
	ffmpegBinary := flag.String("ffmpeg", defaultFFmpegBinary(), "path to the FFmpeg executable")
	enableControl := flag.Bool("enable-control", false, "allow the local desktop client to inject mouse and keyboard input after explicit client consent")
	flag.Parse()

	logger := log.New(os.Stdout, "motionlink: ", log.LstdFlags|log.Lmicroseconds)
	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ffmpegManager := ffmpeg.NewManager(*ffmpegBinary, logger)
	host, err := rtc.NewServer(rootContext, rtc.Config{
		RTPPort:        *rtpPort,
		FFmpegManager:  ffmpegManager,
		Logger:         logger,
		ControlEnabled: *enableControl,
	})
	if err != nil {
		logger.Fatalf("create RTC server: %v", err)
	}
	defer host.Close()

	httpServer := signaling.NewHTTPServer(*listenAddress, host, logger)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Printf("signaling is listening on http://%s", *listenAddress)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-rootContext.Done():
		logger.Printf("shutdown requested")
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			logger.Printf("HTTP shutdown: %v", err)
		}
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("HTTP server: %v", err)
		}
	}
}

func defaultFFmpegBinary() string {
	localBinary := filepath.Join("components", "ffmpeg", "bin", "ffmpeg.exe")
	if _, err := os.Stat(localBinary); err == nil {
		return localBinary
	}

	archiveBinaries, err := filepath.Glob(filepath.Join("components", "ffmpeg", "*", "bin", "ffmpeg.exe"))
	if err == nil && len(archiveBinaries) > 0 {
		return archiveBinaries[0]
	}

	return "ffmpeg"
}
