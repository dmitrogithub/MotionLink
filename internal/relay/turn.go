package relay

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
)

const turnCredentialLifetime = 12 * time.Hour

// TURNConfig configures the embedded TURN server, which tunnels media when
// neither peer can reach the other directly (symmetric NAT, CGNAT, strict
// corporate firewalls). TURN over TCP also passes networks that block UDP.
type TURNConfig struct {
	Port     int
	PublicIP string
	Secret   string
	MinPort  uint16
	MaxPort  uint16
}

// TURN is a running TURN server.
type TURN struct {
	config TURNConfig
	server *turn.Server
}

func StartTURN(config TURNConfig, logger *log.Logger) (*TURN, error) {
	publicIP := net.ParseIP(config.PublicIP)
	if publicIP == nil {
		return nil, fmt.Errorf("TURN needs --public-ip with the server's public address, got %q", config.PublicIP)
	}
	address := net.JoinHostPort("0.0.0.0", strconv.Itoa(config.Port))
	udpListener, err := net.ListenPacket("udp4", address)
	if err != nil {
		return nil, fmt.Errorf("listen TURN UDP %s: %w", address, err)
	}
	tcpListener, err := net.Listen("tcp4", address)
	if err != nil {
		_ = udpListener.Close()
		return nil, fmt.Errorf("listen TURN TCP %s: %w", address, err)
	}

	generator := func() turn.RelayAddressGenerator {
		return &turn.RelayAddressGeneratorPortRange{
			RelayAddress: publicIP,
			Address:      "0.0.0.0",
			MinPort:      config.MinPort,
			MaxPort:      config.MaxPort,
		}
	}
	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelWarn

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:         "motionlink",
		AuthHandler:   turn.NewLongTermAuthHandler(config.Secret, loggerFactory.NewLogger("turn")),
		LoggerFactory: loggerFactory,
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            udpListener,
			RelayAddressGenerator: generator(),
			PermissionHandler:     publicPeersOnly,
		}},
		ListenerConfigs: []turn.ListenerConfig{{
			Listener:              tcpListener,
			RelayAddressGenerator: generator(),
			PermissionHandler:     publicPeersOnly,
		}},
	})
	if err != nil {
		_ = udpListener.Close()
		_ = tcpListener.Close()
		return nil, fmt.Errorf("start TURN server: %w", err)
	}
	logger.Printf("TURN listening on %s (UDP+TCP), relay ports %d-%d, public IP %s",
		address, config.MinPort, config.MaxPort, publicIP)
	return &TURN{config: config, server: server}, nil
}

// publicPeersOnly stops the relay from being used to reach the relay
// machine's own loopback or private network.
func publicPeersOnly(_ net.Addr, peer net.IP) bool {
	return !(peer.IsLoopback() || peer.IsPrivate() || peer.IsUnspecified() ||
		peer.IsLinkLocalUnicast() || peer.IsLinkLocalMulticast() || peer.IsMulticast())
}

// ICEServers returns STUN and TURN entries with fresh time-limited
// credentials (the TURN REST convention: username is the expiry time).
func (t *TURN) ICEServers() []webrtc.ICEServer {
	username, password, err := turn.GenerateLongTermCredentials(t.config.Secret, turnCredentialLifetime)
	hostPort := net.JoinHostPort(t.config.PublicIP, strconv.Itoa(t.config.Port))
	servers := []webrtc.ICEServer{{URLs: []string{"stun:" + hostPort}}}
	if err != nil {
		return servers
	}
	return append(servers, webrtc.ICEServer{
		URLs: []string{
			"turn:" + hostPort + "?transport=udp",
			"turn:" + hostPort + "?transport=tcp",
		},
		Username:   username,
		Credential: password,
	})
}

func (t *TURN) Close() error {
	return t.server.Close()
}
