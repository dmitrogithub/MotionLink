package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/ffmpeg"
	hostinput "motionlink/internal/input"
)

type Config struct {
	RTPPort        int
	FFmpegManager  *ffmpeg.Manager
	Logger         *log.Logger
	ControlEnabled bool
	Network        NetworkConfig
}

const (
	// gatherTimeout bounds non-trickle ICE gathering; a slow TURN server must
	// not stall the answer, so whatever was gathered by then is sent.
	gatherTimeout = 4 * time.Second
	// reconnectGrace keeps a briefly disconnected session alive, so a phone
	// switching between Wi-Fi and mobile data does not lose the stream.
	reconnectGrace = 15 * time.Second
)

// Server serves one client at a time and owns its FFmpeg capture lifecycle.
// Offers reach it only after the signaling layer has verified the access key,
// so a new authenticated client replaces the previous session; this is what
// lets a client reconnect immediately after a network change.
type Server struct {
	context context.Context
	config  Config
	network *networkStack

	mu     sync.Mutex
	active *session
	closed bool
}

type session struct {
	server  *Server
	context context.Context
	cancel  context.CancelFunc
	peer    *webrtc.PeerConnection
	relay   *RTPRelay
	control *hostinput.Controller

	captureMu      sync.Mutex
	profile        ffmpeg.Profile
	captureStarted bool
	graceTimer     *time.Timer

	closeOnce sync.Once
}

func NewServer(parentContext context.Context, config Config) (*Server, error) {
	if parentContext == nil {
		return nil, errors.New("server context is required")
	}
	if config.RTPPort < 1 || config.RTPPort > 65535 {
		return nil, fmt.Errorf("invalid RTP port %d", config.RTPPort)
	}
	if config.FFmpegManager == nil || config.Logger == nil {
		return nil, errors.New("FFmpeg manager and logger are required")
	}
	network, err := newNetworkStack(config.Network, config.Logger)
	if err != nil {
		return nil, err
	}
	return &Server{context: parentContext, config: config, network: network}, nil
}

// ControlEnabled reports whether the host allows remote input at all.
func (s *Server) ControlEnabled() bool {
	return s.config.ControlEnabled
}

// AcceptOffer creates an answer for an authenticated client. extraICEServers
// are per-session servers, such as TURN credentials issued by a relay.
func (s *Server) AcceptOffer(offer webrtc.SessionDescription, quality string, extraICEServers []webrtc.ICEServer) (webrtc.SessionDescription, error) {
	if offer.Type != webrtc.SDPTypeOffer || offer.SDP == "" {
		return webrtc.SessionDescription{}, errors.New("a non-empty WebRTC offer is required")
	}
	profile, err := ffmpeg.LookupProfile(quality)
	if err != nil {
		return webrtc.SessionDescription{}, err
	}

	session, err := s.newSession(profile, extraICEServers)
	if err != nil {
		return webrtc.SessionDescription{}, err
	}

	if err := session.peer.SetRemoteDescription(offer); err != nil {
		session.close("invalid remote offer")
		return webrtc.SessionDescription{}, fmt.Errorf("set remote description: %w", err)
	}

	answer, err := session.peer.CreateAnswer(nil)
	if err != nil {
		session.close("could not create answer")
		return webrtc.SessionDescription{}, fmt.Errorf("create answer: %w", err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(session.peer)
	if err := session.peer.SetLocalDescription(answer); err != nil {
		session.close("could not set answer")
		return webrtc.SessionDescription{}, fmt.Errorf("set local description: %w", err)
	}
	select {
	case <-gatherComplete:
	case <-time.After(gatherTimeout):
		s.config.Logger.Printf("ICE gathering is slow; answering with the candidates gathered so far")
	case <-s.context.Done():
		session.close("server stopped while gathering ICE candidates")
		return webrtc.SessionDescription{}, s.context.Err()
	}

	localDescription := session.peer.LocalDescription()
	if localDescription == nil {
		session.close("answer has no local description")
		return webrtc.SessionDescription{}, errors.New("WebRTC answer has no local description")
	}
	return *localDescription, nil
}

func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	active := s.active
	s.mu.Unlock()
	if active != nil {
		active.close("server stopped")
	}
	s.network.close()
}

func (s *Server) newSession(profile ffmpeg.Profile, extraICEServers []webrtc.ICEServer) (*session, error) {
	s.mu.Lock()
	previous := s.active
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, errors.New("RTC server is closed")
	}
	// The previous session must release the RTP port and FFmpeg before the
	// new one claims them.
	if previous != nil {
		previous.close("replaced by a new authenticated client")
	}

	iceServers := append(append([]webrtc.ICEServer(nil), s.config.Network.ICEServers...), extraICEServers...)
	peer, err := s.network.api.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90_000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	}, "screen", "motionlink")
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("create H.264 RTP track: %w", err)
	}
	relay, err := NewRTPRelay(s.config.RTPPort, track, s.config.Logger)
	if err != nil {
		_ = peer.Close()
		return nil, err
	}

	sessionContext, cancel := context.WithCancel(s.context)
	current := &session{
		server:  s,
		context: sessionContext,
		cancel:  cancel,
		peer:    peer,
		relay:   relay,
		control: hostinput.NewController(s.config.ControlEnabled, s.config.Logger),
		profile: profile,
	}

	sender, err := peer.AddTrack(track)
	if err != nil {
		cancel()
		relay.Close()
		_ = peer.Close()
		return nil, fmt.Errorf("add H.264 track: %w", err)
	}

	s.mu.Lock()
	if s.closed || s.active != nil {
		s.mu.Unlock()
		cancel()
		relay.Close()
		_ = peer.Close()
		return nil, errors.New("another client connected at the same moment; retry")
	}
	s.active = current
	s.mu.Unlock()

	s.configurePeerConnection(current)
	relay.Start()
	go drainRTCP(sender)

	return current, nil
}

func (s *Server) configurePeerConnection(current *session) {
	current.peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() != "motion-input" {
			s.config.Logger.Printf("rejecting unexpected data channel %q", channel.Label())
			_ = channel.Close()
			return
		}

		channel.OnOpen(func() {
			s.config.Logger.Printf("motion-input data channel opened")
			current.sendHello(channel)
		})
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			if !message.IsString {
				s.config.Logger.Printf("ignoring binary motion-input message")
				return
			}
			if current.handleSessionMessage(channel, message.Data) {
				return
			}
			event, err := hostinput.Decode(message.Data)
			if err != nil {
				s.config.Logger.Printf("invalid motion-input event: %v", err)
				return
			}
			result, err := current.control.Handle(event)
			if err != nil {
				s.config.Logger.Printf("apply motion-input event %q: %v", event.Type, err)
			}
			if event.Type == hostinput.EventControl {
				current.send(channel, controlStatusMessage{
					Type:    "control-status",
					Enabled: result.Enabled,
					Message: result.Message,
				})
			}
		})
	})

	current.peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected {
			current.logSelectedPath()
		}
	})

	current.peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.config.Logger.Printf("WebRTC connection state: %s", state.String())
		switch state {
		case webrtc.PeerConnectionStateConnected:
			current.stopGraceTimer()
			go current.startCapture()
		case webrtc.PeerConnectionStateDisconnected:
			current.startGraceTimer()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			go current.close("client disconnected")
		}
	})
}

type controlStatusMessage struct {
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	Message string `json:"message"`
}

type helloMessage struct {
	Type     string           `json:"type"`
	Quality  string           `json:"quality"`
	Profiles []ffmpeg.Profile `json:"profiles"`
	Control  bool             `json:"control"`
}

type qualityStatusMessage struct {
	Type    string `json:"type"`
	Quality string `json:"quality"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type sessionMessage struct {
	Type    string          `json:"type"`
	Quality string          `json:"quality"`
	Time    json.RawMessage `json:"t"`
}

// handleSessionMessage processes non-input messages (quality and ping). It
// returns false for anything that should go through the input validator.
func (s *session) handleSessionMessage(channel *webrtc.DataChannel, payload []byte) bool {
	if len(payload) > 1024 {
		return false
	}
	var message sessionMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return false
	}
	switch message.Type {
	case "ping":
		s.send(channel, struct {
			Type string          `json:"type"`
			Time json.RawMessage `json:"t"`
		}{Type: "pong", Time: message.Time})
		return true
	case "quality":
		go s.changeQuality(channel, message.Quality)
		return true
	default:
		return false
	}
}

func (s *session) sendHello(channel *webrtc.DataChannel) {
	s.captureMu.Lock()
	quality := s.profile.ID
	s.captureMu.Unlock()
	s.send(channel, helloMessage{
		Type:     "hello",
		Quality:  quality,
		Profiles: ffmpeg.Profiles(),
		Control:  s.server.config.ControlEnabled,
	})
}

func (s *session) changeQuality(channel *webrtc.DataChannel, quality string) {
	profile, err := ffmpeg.LookupProfile(quality)
	if err != nil {
		s.send(channel, qualityStatusMessage{Type: "quality-status", Quality: quality, Message: err.Error()})
		return
	}

	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	if s.context.Err() != nil {
		return
	}
	if profile.ID == s.profile.ID {
		s.send(channel, qualityStatusMessage{Type: "quality-status", Quality: profile.ID, OK: true})
		return
	}
	previous := s.profile
	s.profile = profile
	if !s.captureStarted {
		s.send(channel, qualityStatusMessage{Type: "quality-status", Quality: profile.ID, OK: true})
		return
	}

	manager := s.server.config.FFmpegManager
	manager.Stop()
	if err := manager.Start(s.context, s.server.config.RTPPort, profile); err != nil {
		s.server.config.Logger.Printf("restart capture with %s: %v; restoring %s", profile.ID, err, previous.ID)
		s.profile = previous
		if restoreErr := manager.Start(s.context, s.server.config.RTPPort, previous); restoreErr != nil {
			s.server.config.Logger.Printf("cannot restore capture: %v", restoreErr)
			go s.close("FFmpeg did not restart")
		}
		s.send(channel, qualityStatusMessage{Type: "quality-status", Quality: previous.ID, Message: "could not switch quality: " + err.Error()})
		return
	}
	s.send(channel, qualityStatusMessage{Type: "quality-status", Quality: profile.ID, OK: true})
}

func (s *session) send(channel *webrtc.DataChannel, message any) {
	payload, err := json.Marshal(message)
	if err != nil {
		s.server.config.Logger.Printf("encode data channel message: %v", err)
		return
	}
	if err := channel.SendText(string(payload)); err != nil {
		s.server.config.Logger.Printf("send data channel message: %v", err)
	}
}

func (s *session) startCapture() {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	// ICE may reconnect after a brief outage; the capture keeps running.
	if s.captureStarted || s.context.Err() != nil {
		return
	}
	if err := s.server.config.FFmpegManager.Start(s.context, s.server.config.RTPPort, s.profile); err != nil {
		s.server.config.Logger.Printf("cannot start desktop capture: %v", err)
		go s.close("FFmpeg did not start")
		return
	}
	s.captureStarted = true
}

func (s *session) startGraceTimer() {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	if s.graceTimer != nil {
		return
	}
	s.server.config.Logger.Printf("client connection interrupted; waiting %s for it to recover", reconnectGrace)
	s.graceTimer = time.AfterFunc(reconnectGrace, func() {
		s.close("client did not reconnect")
	})
}

func (s *session) stopGraceTimer() {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	if s.graceTimer != nil {
		s.graceTimer.Stop()
		s.graceTimer = nil
		s.server.config.Logger.Printf("client connection recovered")
	}
}

// logSelectedPath reports whether the session runs directly, through NAT
// traversal or through a TURN relay. It is the first thing to check when a
// remote connection is slow.
func (s *session) logSelectedPath() {
	sctp := s.peer.SCTP()
	if sctp == nil || sctp.Transport() == nil || sctp.Transport().ICETransport() == nil {
		return
	}
	pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return
	}
	s.server.config.Logger.Printf("network path: local %s/%s %s:%d <-> remote %s/%s %s:%d",
		pair.Local.Typ, pair.Local.Protocol, pair.Local.Address, pair.Local.Port,
		pair.Remote.Typ, pair.Remote.Protocol, pair.Remote.Address, pair.Remote.Port)
}

func (s *session) close(reason string) {
	s.closeOnce.Do(func() {
		s.server.config.Logger.Printf("closing WebRTC session: %s", reason)
		if err := s.control.ReleaseAll(); err != nil {
			s.server.config.Logger.Printf("release remote input: %v", err)
		}
		s.cancel()

		s.captureMu.Lock()
		if s.graceTimer != nil {
			s.graceTimer.Stop()
		}
		s.server.config.FFmpegManager.Stop()
		s.captureMu.Unlock()

		s.relay.Close()
		_ = s.peer.Close()

		s.server.mu.Lock()
		if s.server.active == s {
			s.server.active = nil
		}
		s.server.mu.Unlock()
	})
}

func drainRTCP(sender *webrtc.RTPSender) {
	buffer := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buffer); err != nil {
			return
		}
	}
}
