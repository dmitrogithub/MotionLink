package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/signaling"
)

const (
	pollTimeout      = 25 * time.Second
	answerTimeout    = 20 * time.Second
	roomOnlineWindow = 40 * time.Second
	maxPending       = 8
	maxRelayBody     = 1 << 20
	offersPerMinute  = 30
	maxRooms         = 10_000
)

// ServerConfig configures the rendezvous HTTP API.
type ServerConfig struct {
	Name    string
	Version string
	// ICEServers returns the STUN/TURN servers (with fresh credentials) handed
	// to both peers of a room.
	ICEServers func() []webrtc.ICEServer
	// PublicICEServers are credential-free servers listed in /api/info.
	PublicICEServers []webrtc.ICEServer
	WebAssets        fs.FS
	Logger           *log.Logger
	// TrustProxy takes the client address from X-Forwarded-For; enable it
	// only behind a reverse proxy such as Caddy or nginx.
	TrustProxy bool
}

// Server is the rendezvous service. It keeps only in-memory state: rooms
// disappear shortly after their host stops polling.
type Server struct {
	config ServerConfig

	mu      sync.Mutex
	rooms   map[string]*room
	clients map[string]*rateWindow
}

type room struct {
	lastPoll time.Time
	pollers  int
	pending  chan *pendingOffer
	waiting  map[string]*pendingOffer
}

type pendingOffer struct {
	id    string
	from  string
	offer json.RawMessage
	reply chan Reply
}

type rateWindow struct {
	count int
	since time.Time
}

func NewServer(config ServerConfig) *Server {
	return &Server{
		config:  config,
		rooms:   make(map[string]*room),
		clients: make(map[string]*rateWindow),
	}
}

// Handler returns the relay HTTP API, plus the web client when configured.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/relay/{room}/status", s.status)
	mux.HandleFunc("GET /api/relay/{room}/ice", s.ice)
	mux.HandleFunc("GET /api/relay/{room}/poll", s.poll)
	mux.HandleFunc("POST /api/relay/{room}/answer/{id}", s.answer)
	mux.HandleFunc("POST /api/relay/{room}/offer", s.offer)
	if s.config.WebAssets != nil {
		mux.Handle("GET /", signaling.StaticHandler(s.config.WebAssets))
	}
	return signaling.WithCORS(mux)
}

func (s *Server) info(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"role":       "relay",
		"name":       s.config.Name,
		"version":    s.config.Version,
		"iceServers": s.config.PublicICEServers,
	})
}

func (s *Server) status(writer http.ResponseWriter, request *http.Request) {
	roomID, ok := roomFrom(writer, request)
	if !ok {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"online": s.online(roomID)})
}

// ice hands TURN credentials to a client, but only for a room whose host is
// online, so the relay is not an open TURN service for arbitrary callers.
func (s *Server) ice(writer http.ResponseWriter, request *http.Request) {
	roomID, ok := roomFrom(writer, request)
	if !ok {
		return
	}
	if !s.online(roomID) {
		writeError(writer, http.StatusNotFound, "host-offline", "the host with this key is not online")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"iceServers": s.iceServers()})
}

func (s *Server) poll(writer http.ResponseWriter, request *http.Request) {
	roomID, ok := roomFrom(writer, request)
	if !ok {
		return
	}
	current, err := s.enterPoll(roomID)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "full", err.Error())
		return
	}
	defer s.leavePoll(roomID)

	timer := time.NewTimer(pollTimeout)
	defer timer.Stop()
	for {
		select {
		case pending := <-current.pending:
			// A client that already gave up must not reach the host: its
			// offer would replace the session of the client now connected.
			if !s.stillWaiting(roomID, pending.id) {
				continue
			}
			writeJSON(writer, http.StatusOK, PollResponse{
				ID:         pending.id,
				From:       pending.from,
				Offer:      pending.offer,
				ICEServers: s.iceServers(),
			})
		case <-timer.C:
			writer.WriteHeader(http.StatusNoContent)
		case <-request.Context().Done():
		}
		return
	}
}

func (s *Server) stillWaiting(roomID, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.rooms[roomID]
	if current == nil {
		return false
	}
	_, ok := current.waiting[id]
	return ok
}

func (s *Server) answer(writer http.ResponseWriter, request *http.Request) {
	roomID, ok := roomFrom(writer, request)
	if !ok {
		return
	}
	var reply Reply
	if err := json.NewDecoder(io.LimitReader(request.Body, maxRelayBody)).Decode(&reply); err != nil || reply.Status < 200 || reply.Status > 599 {
		writeError(writer, http.StatusBadRequest, "bad-request", "invalid host reply")
		return
	}

	s.mu.Lock()
	var waiting *pendingOffer
	if current := s.rooms[roomID]; current != nil {
		waiting = current.waiting[request.PathValue("id")]
		delete(current.waiting, request.PathValue("id"))
	}
	s.mu.Unlock()
	if waiting == nil {
		writeError(writer, http.StatusGone, "expired", "the client is no longer waiting")
		return
	}
	waiting.reply <- reply
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) offer(writer http.ResponseWriter, request *http.Request) {
	roomID, ok := roomFrom(writer, request)
	if !ok {
		return
	}
	from := s.clientIP(request)
	if !s.allowOffer(from) {
		writeError(writer, http.StatusTooManyRequests, "rate-limited", "too many connection attempts, try again in a minute")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxRelayBody))
	if err != nil || !json.Valid(body) {
		writeError(writer, http.StatusBadRequest, "bad-request", "invalid offer JSON")
		return
	}

	pending := &pendingOffer{id: randomID(), from: from, offer: body, reply: make(chan Reply, 1)}
	if !s.enqueue(roomID, pending) {
		writeError(writer, http.StatusNotFound, "host-offline", "the host with this key is not online")
		return
	}
	defer s.forget(roomID, pending.id)

	timer := time.NewTimer(answerTimeout)
	defer timer.Stop()
	select {
	case reply := <-pending.reply:
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(reply.Status)
		_, _ = writer.Write(reply.Body)
	case <-timer.C:
		writeError(writer, http.StatusGatewayTimeout, "host-timeout", "the host did not answer in time")
	case <-request.Context().Done():
	}
}

func (s *Server) enterPoll(roomID string) (*room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	current := s.rooms[roomID]
	if current == nil {
		if len(s.rooms) >= maxRooms {
			return nil, errFull
		}
		current = &room{pending: make(chan *pendingOffer, maxPending), waiting: make(map[string]*pendingOffer)}
		s.rooms[roomID] = current
	}
	current.pollers++
	current.lastPoll = time.Now()
	return current, nil
}

func (s *Server) leavePoll(roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.rooms[roomID]; current != nil {
		current.pollers--
		current.lastPoll = time.Now()
	}
}

func (s *Server) online(roomID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.rooms[roomID]
	return current != nil && (current.pollers > 0 || time.Since(current.lastPoll) < roomOnlineWindow)
}

func (s *Server) enqueue(roomID string, pending *pendingOffer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.rooms[roomID]
	if current == nil || (current.pollers == 0 && time.Since(current.lastPoll) >= roomOnlineWindow) {
		return false
	}
	select {
	case current.pending <- pending:
		current.waiting[pending.id] = pending
		return true
	default:
		return false
	}
}

func (s *Server) forget(roomID, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.rooms[roomID]; current != nil {
		delete(current.waiting, id)
	}
}

func (s *Server) allowOffer(from string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	window := s.clients[from]
	if window == nil || now.Sub(window.since) > time.Minute {
		if len(s.clients) >= maxRooms {
			s.pruneLocked(now)
		}
		window = &rateWindow{since: now}
		s.clients[from] = window
	}
	window.count++
	return window.count <= offersPerMinute
}

func (s *Server) pruneLocked(now time.Time) {
	for roomID, current := range s.rooms {
		if current.pollers == 0 && now.Sub(current.lastPoll) > 2*roomOnlineWindow && len(current.waiting) == 0 {
			delete(s.rooms, roomID)
		}
	}
	for from, window := range s.clients {
		if now.Sub(window.since) > time.Minute {
			delete(s.clients, from)
		}
	}
}

func (s *Server) iceServers() []webrtc.ICEServer {
	if s.config.ICEServers == nil {
		return s.config.PublicICEServers
	}
	return s.config.ICEServers()
}

// Run serves the relay until ctx is cancelled.
func (s *Server) Run(ctx context.Context, server *http.Server, certFile, keyFile string) error {
	errorsChannel := make(chan error, 1)
	go func() {
		if certFile != "" {
			errorsChannel <- server.ListenAndServeTLS(certFile, keyFile)
			return
		}
		errorsChannel <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case err := <-errorsChannel:
		return err
	}
}

type relayError string

func (e relayError) Error() string { return string(e) }

const errFull = relayError("the relay is at capacity")

func roomFrom(writer http.ResponseWriter, request *http.Request) (string, bool) {
	roomID := request.PathValue("room")
	if !ValidRoom(roomID) {
		writeError(writer, http.StatusBadRequest, "bad-room", "invalid room")
		return "", false
	}
	return roomID, true
}

func (s *Server) clientIP(request *http.Request) string {
	if s.config.TrustProxy {
		if forwarded := request.Header.Get("X-Forwarded-For"); forwarded != "" {
			first, _, _ := strings.Cut(forwarded, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

func randomID() string {
	buffer := make([]byte, 12)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"code": code, "error": message})
}
