package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/ffmpeg"
	"motionlink/internal/relay"
	"motionlink/internal/rtc"
	"motionlink/internal/signaling"
	"motionlink/web"
)

const version = "2.0.0"

func main() {
	listenAddress := flag.String("listen", "0.0.0.0:8080", "HTTP signaling and web client address; use 127.0.0.1:8080 for this PC only")
	rtpPort := flag.Int("rtp-port", 5004, "local UDP port that receives H.264 RTP from FFmpeg")
	ffmpegBinary := flag.String("ffmpeg", defaultFFmpegBinary(), "path to the FFmpeg executable")
	enableControl := flag.Bool("enable-control", false, "allow authenticated clients to inject mouse and keyboard input after explicit client consent")
	keyFlag := flag.String("key", "", "access key clients must type in (saved for next runs); empty uses the saved or a new random key")
	newKey := flag.Bool("new-key", false, "generate and save a new random access key")
	icePort := flag.Int("ice-port", 50000, "single UDP+TCP port for WebRTC media (forward it on the router for Internet access); 0 = random UDP ports")
	publicIPs := flag.String("public-ip", "", "comma-separated public IP(s) to announce, e.g. when --ice-port is forwarded on the router")
	stunServers := flag.String("stun", "stun:stun.l.google.com:19302,stun:stun.cloudflare.com:3478", "comma-separated STUN servers; empty disables STUN")
	turnURL := flag.String("turn", "", "optional TURN server URL(s), comma-separated, e.g. turn:example.com:3478?transport=udp")
	turnUser := flag.String("turn-user", "", "TURN username")
	turnPassword := flag.String("turn-pass", "", "TURN password")
	relayURL := flag.String("relay", "", "MotionLink relay address for connections from other networks, e.g. https://relay.example.com")
	hostName := flag.String("name", defaultHostName(), "name shown to clients")
	noWeb := flag.Bool("no-web", false, "do not serve the browser client")
	flag.Parse()

	logger := log.New(os.Stdout, "motionlink: ", log.LstdFlags|log.Lmicroseconds)
	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	key, err := resolveKey(*keyFlag, *newKey)
	if err != nil {
		logger.Fatalf("access key: %v", err)
	}

	network := rtc.NetworkConfig{
		ICEPort:    *icePort,
		PublicIPs:  splitList(*publicIPs),
		ICEServers: iceServers(*stunServers, *turnURL, *turnUser, *turnPassword),
	}

	ffmpegManager := ffmpeg.NewManager(*ffmpegBinary, logger)
	go func() {
		// Probe the encoder now so the first client does not wait for it.
		if check, err := ffmpegManager.Check(rootContext); err != nil {
			logger.Printf("WARNING: %v", err)
		} else {
			logger.Printf("FFmpeg ready: %s (%s)", check.Path, check.Encoder)
		}
	}()

	host, err := rtc.NewServer(rootContext, rtc.Config{
		RTPPort:        *rtpPort,
		FFmpegManager:  ffmpegManager,
		Logger:         logger,
		ControlEnabled: *enableControl,
		Network:        network,
	})
	if err != nil {
		logger.Fatalf("create RTC server: %v", err)
	}
	defer host.Close()

	answerer := signaling.NewAnswerer(key, host, logger)
	httpConfig := signaling.HTTPConfig{
		Address:  *listenAddress,
		Answerer: answerer,
		Logger:   logger,
		Info: signaling.Info{
			Role:       "host",
			Name:       *hostName,
			Version:    version,
			Control:    *enableControl,
			Profiles:   ffmpeg.Profiles(),
			ICEServers: credentialFree(network.ICEServers),
		},
	}
	if !*noWeb {
		httpConfig.WebAssets = web.Assets()
	}
	httpServer := signaling.NewHTTPServer(httpConfig)

	if *relayURL != "" {
		relayClient, err := relay.NewClient(*relayURL, key, answerer, logger)
		if err != nil {
			logger.Fatalf("relay: %v", err)
		}
		go relayClient.Run(rootContext)
	}

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.ListenAndServe()
	}()
	printBanner(key, *listenAddress, *relayURL, *enableControl, !*noWeb)

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

func iceServers(stun, turnURLs, turnUser, turnPassword string) []webrtc.ICEServer {
	var servers []webrtc.ICEServer
	if urls := splitList(stun); len(urls) > 0 {
		servers = append(servers, webrtc.ICEServer{URLs: urls})
	}
	if urls := splitList(turnURLs); len(urls) > 0 {
		servers = append(servers, webrtc.ICEServer{URLs: urls, Username: turnUser, Credential: turnPassword})
	}
	return servers
}

// credentialFree keeps static TURN credentials out of the public /api/info.
func credentialFree(servers []webrtc.ICEServer) []webrtc.ICEServer {
	result := []webrtc.ICEServer{}
	for _, server := range servers {
		if server.Username == "" && server.Credential == nil {
			result = append(result, server)
		}
	}
	return result
}

func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func defaultHostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "MotionLink host"
	}
	return name
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

func printBanner(key, listenAddress, relayURL string, control, webClient bool) {
	fmt.Println()
	fmt.Println("  ┌──────────────────────────────────────────────┐")
	fmt.Printf("  │  MotionLink host %-28s│\n", version)
	fmt.Printf("  │  Access key:  %-31s│\n", key)
	fmt.Println("  └──────────────────────────────────────────────┘")
	if control {
		fmt.Println("  Remote control: allowed after the client confirms it")
	} else {
		fmt.Println("  Remote control: off (restart with --enable-control to allow it)")
	}

	links := lanLinks(listenAddress, key)
	if webClient && len(links) > 0 {
		fmt.Println()
		fmt.Println("  Phone or another PC on this network — open in a browser:")
		for _, link := range links {
			fmt.Println("    " + link)
		}
		printQR(links[0])
	}
	if relayURL != "" {
		if base, err := relay.ParseURL(relayURL); err == nil {
			fmt.Println()
			fmt.Println("  From any network (through the relay):")
			fmt.Printf("    %s/#key=%s\n", base.String(), key)
		}
	}
	fmt.Println()
	fmt.Println("  Desktop app: enter this PC's address (or the relay) and the access key.")
	fmt.Println()
}
