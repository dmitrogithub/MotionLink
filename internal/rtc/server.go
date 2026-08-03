package rtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/ffmpeg"
	hostinput "motionlink/internal/input"
)

// ClientAlreadyConnectedError is returned while a browser session is active.
// A typed error avoids a package-level mutable sentinel.
type ClientAlreadyConnectedError struct{}

func (ClientAlreadyConnectedError) Error() string {
	return "a browser client is already connected or connecting"
}

type Config struct {
	RTPPort        int
	FFmpegManager  *ffmpeg.Manager
	Logger         *log.Logger
	ControlEnabled bool
}

// Server accepts one WebRTC client and owns its FFmpeg capture lifecycle.
type Server struct {
	context context.Context
	config  Config

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
	return &Server{context: parentContext, config: config}, nil
}

// AcceptOffer creates an answer for exactly one active browser client.
func (s *Server) AcceptOffer(offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if offer.Type != webrtc.SDPTypeOffer || offer.SDP == "" {
		return webrtc.SessionDescription{}, errors.New("a non-empty WebRTC offer is required")
	}

	session, err := s.newSession()
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
}

func (s *Server) newSession() (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errors.New("RTC server is closed")
	}
	if s.active != nil {
		return nil, ClientAlreadyConnectedError{}
	}

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
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
	}

	sender, err := peer.AddTrack(track)
	if err != nil {
		cancel()
		relay.Close()
		_ = peer.Close()
		return nil, fmt.Errorf("add H.264 track: %w", err)
	}
	s.active = current
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
		})
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			if !message.IsString {
				s.config.Logger.Printf("ignoring binary motion-input message")
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
				current.sendControlStatus(channel, result)
			}
		})
	})

	current.peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.config.Logger.Printf("WebRTC connection state: %s", state.String())
		switch state {
		case webrtc.PeerConnectionStateConnected:
			go current.startCapture()
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			go current.close("browser client disconnected")
		}
	})
}

func (s *session) sendControlStatus(channel *webrtc.DataChannel, result hostinput.Result) {
	payload, err := json.Marshal(struct {
		Type    string `json:"type"`
		Enabled bool   `json:"enabled"`
		Message string `json:"message"`
	}{
		Type:    "control-status",
		Enabled: result.Enabled,
		Message: result.Message,
	})
	if err != nil {
		s.server.config.Logger.Printf("encode control status: %v", err)
		return
	}
	if err := channel.SendText(string(payload)); err != nil {
		s.server.config.Logger.Printf("send control status: %v", err)
	}
}

func (s *session) startCapture() {
	if err := s.server.config.FFmpegManager.Start(s.context, s.server.config.RTPPort); err != nil {
		s.server.config.Logger.Printf("cannot start desktop capture: %v", err)
		s.close("FFmpeg did not start")
	}
}

func (s *session) close(reason string) {
	s.closeOnce.Do(func() {
		s.server.config.Logger.Printf("closing WebRTC session: %s", reason)
		if err := s.control.ReleaseAll(); err != nil {
			s.server.config.Logger.Printf("release remote input: %v", err)
		}
		s.cancel()
		s.server.config.FFmpegManager.Stop()
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
			if !errors.Is(err, io.EOF) {
				return
			}
			return
		}
	}
}
