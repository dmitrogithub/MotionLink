package rtc

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// NetworkConfig controls how the host can be reached. A fixed ICEPort lets a
// user forward a single UDP+TCP port on the router; PublicIPs advertises that
// router's address without waiting for STUN.
type NetworkConfig struct {
	// ICEPort carries every WebRTC connection over one UDP port and, as a
	// fallback for networks that block UDP, one TCP port with the same number.
	// Zero uses random UDP ports and disables ICE-TCP.
	ICEPort int
	// PublicIPs are announced as server-reflexive candidates (1:1 NAT or a
	// forwarded port).
	PublicIPs []string
	// ICEServers are STUN/TURN servers the host always uses.
	ICEServers []webrtc.ICEServer
}

const (
	iceDisconnectedTimeout = 5 * time.Second
	iceFailedTimeout       = 25 * time.Second
	iceKeepAlive           = 2 * time.Second
)

type networkStack struct {
	api      *webrtc.API
	udpMux   *ice.MultiUDPMuxDefault
	tcpMux   *ice.TCPMuxDefault
	listener net.Listener
}

func newNetworkStack(config NetworkConfig, logger *log.Logger) (*networkStack, error) {
	settings := webrtc.SettingEngine{}
	settings.SetICETimeouts(iceDisconnectedTimeout, iceFailedTimeout, iceKeepAlive)
	// Answer the client's ICE candidates even before gathering finishes.
	settings.SetHostAcceptanceMinWait(0)
	settings.SetSTUNGatherTimeout(3 * time.Second)

	stack := &networkStack{}
	networkTypes := []webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6}

	if config.ICEPort != 0 {
		if config.ICEPort < 1 || config.ICEPort > 65535 {
			return nil, fmt.Errorf("invalid ICE port %d", config.ICEPort)
		}
		udpMux, err := ice.NewMultiUDPMuxFromPort(config.ICEPort, ice.UDPMuxFromPortWithLoopback())
		if err != nil {
			return nil, fmt.Errorf("listen for WebRTC on UDP port %d: %w", config.ICEPort, err)
		}
		stack.udpMux = udpMux
		settings.SetICEUDPMux(udpMux)

		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", config.ICEPort))
		if err != nil {
			stack.close()
			return nil, fmt.Errorf("listen for WebRTC on TCP port %d: %w", config.ICEPort, err)
		}
		stack.listener = listener
		stack.tcpMux = ice.NewTCPMuxDefault(ice.TCPMuxParams{Listener: listener, ReadBufferSize: 64, WriteBufferSize: 4 << 20})
		settings.SetICETCPMux(stack.tcpMux)
		networkTypes = append(networkTypes, webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6)
		logger.Printf("WebRTC media port: %d (UDP, with TCP fallback)", config.ICEPort)
	}
	settings.SetNetworkTypes(networkTypes)

	if len(config.PublicIPs) > 0 {
		settings.SetNAT1To1IPs(config.PublicIPs, webrtc.ICECandidateTypeSrflx)
	}

	// The default codecs and interceptors give the video track NACK
	// retransmission, RTCP reports and congestion feedback, which matter far
	// more over the Internet than on localhost.
	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		stack.close()
		return nil, fmt.Errorf("register codecs: %w", err)
	}
	interceptors := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(media, interceptors); err != nil {
		stack.close()
		return nil, fmt.Errorf("register interceptors: %w", err)
	}

	stack.api = webrtc.NewAPI(
		webrtc.WithSettingEngine(settings),
		webrtc.WithMediaEngine(media),
		webrtc.WithInterceptorRegistry(interceptors),
	)
	return stack, nil
}

func (s *networkStack) close() {
	if s.tcpMux != nil {
		_ = s.tcpMux.Close()
	} else if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.udpMux != nil {
		_ = s.udpMux.Close()
	}
}
