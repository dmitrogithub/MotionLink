// Command relay runs the MotionLink rendezvous server on a machine with a
// public IP address. Hosts behind NAT register with it, clients find them by
// access key, and its built-in TURN server tunnels media when a direct
// peer-to-peer path is impossible.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/relay"
	"motionlink/web"
)

const version = "2.0.0"

func main() {
	listenAddress := flag.String("listen", ":8080", "HTTP(S) address of the relay API and web client")
	certFile := flag.String("tls-cert", "", "TLS certificate file (HTTPS is required to install the phone app as a PWA)")
	keyFile := flag.String("tls-key", "", "TLS private key file")
	publicIP := flag.String("public-ip", "", "public IPv4 address of this server; required for TURN")
	turnPort := flag.Int("turn-port", 3478, "TURN/STUN port (UDP and TCP); 0 disables the embedded TURN server")
	turnSecret := flag.String("turn-secret", "", "shared secret for TURN credentials; empty generates one per start")
	relayPorts := flag.String("relay-ports", "49160-49300", "UDP port range for TURN relay allocations (open it in the firewall)")
	trustProxy := flag.Bool("trust-proxy", false, "take client IPs from X-Forwarded-For (only behind a reverse proxy)")
	noWeb := flag.Bool("no-web", false, "do not serve the browser client")
	flag.Parse()

	logger := log.New(os.Stdout, "motionlink-relay: ", log.LstdFlags|log.Lmicroseconds)
	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	config := relay.ServerConfig{
		Name:       "MotionLink relay",
		Version:    version,
		Logger:     logger,
		TrustProxy: *trustProxy,
		PublicICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}
	if !*noWeb {
		config.WebAssets = web.Assets()
	}

	if *turnPort != 0 {
		minPort, maxPort, err := parseRange(*relayPorts)
		if err != nil {
			logger.Fatalf("--relay-ports: %v", err)
		}
		secret := *turnSecret
		if secret == "" {
			secret = randomSecret()
		}
		turnServer, err := relay.StartTURN(relay.TURNConfig{
			Port:     *turnPort,
			PublicIP: *publicIP,
			Secret:   secret,
			MinPort:  minPort,
			MaxPort:  maxPort,
		}, logger)
		if err != nil {
			logger.Fatalf("%v (use --turn-port 0 to run without TURN)", err)
		}
		defer turnServer.Close()
		config.ICEServers = turnServer.ICEServers
		config.PublicICEServers = []webrtc.ICEServer{{URLs: []string{fmt.Sprintf("stun:%s:%d", *publicIP, *turnPort)}}}
	}

	server := relay.NewServer(config)
	httpServer := &http.Server{
		Addr:              *listenAddress,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	scheme := "http"
	if *certFile != "" {
		scheme = "https"
	}
	logger.Printf("relay %s listening on %s://%s", version, scheme, *listenAddress)
	if err := server.Run(rootContext, httpServer, *certFile, *keyFile); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("HTTP server: %v", err)
	}
}

func parseRange(value string) (uint16, uint16, error) {
	low, high, ok := strings.Cut(value, "-")
	if !ok {
		return 0, 0, fmt.Errorf("expected MIN-MAX, got %q", value)
	}
	minPort, err := strconv.ParseUint(strings.TrimSpace(low), 10, 16)
	if err != nil {
		return 0, 0, err
	}
	maxPort, err := strconv.ParseUint(strings.TrimSpace(high), 10, 16)
	if err != nil {
		return 0, 0, err
	}
	if minPort == 0 || maxPort < minPort {
		return 0, 0, fmt.Errorf("invalid port range %q", value)
	}
	return uint16(minPort), uint16(maxPort), nil
}

func randomSecret() string {
	buffer := make([]byte, 24)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}
