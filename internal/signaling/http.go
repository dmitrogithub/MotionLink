package signaling

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/auth"
	"motionlink/internal/ffmpeg"
)

// Info is published at GET /api/info. It contains nothing secret: the web
// client uses it to pick its connection mode and list the quality presets.
type Info struct {
	Role       string             `json:"role"`
	Name       string             `json:"name"`
	Version    string             `json:"version"`
	Control    bool               `json:"control"`
	Profiles   []ffmpeg.Profile   `json:"profiles"`
	ICEServers []webrtc.ICEServer `json:"iceServers"`
}

// HTTPConfig configures the host's own signaling endpoint.
type HTTPConfig struct {
	Address  string
	Answerer *Answerer
	Info     Info
	// WebAssets is the built web client. It lets a phone or another PC open
	// the host address in a browser without installing anything.
	WebAssets fs.FS
	Logger    *log.Logger
}

// NewHTTPServer creates the host endpoint used for non-trickle offer/answer
// signaling. It deliberately does not use a package-global mux.
func NewHTTPServer(config HTTPConfig) *http.Server {
	handler := &handler{answerer: config.Answerer, info: config.Info, logger: config.Logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.HandleFunc("GET /api/info", handler.infoHandler)
	mux.HandleFunc("POST /api/offer", handler.offer)
	if config.WebAssets != nil {
		mux.Handle("GET /", StaticHandler(config.WebAssets))
	}

	return &http.Server{
		Addr:              config.Address,
		Handler:           WithCORS(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
}

type handler struct {
	answerer *Answerer
	info     Info
	logger   *log.Logger
}

func (h *handler) health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"ok":true}`))
}

func (h *handler) infoHandler(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, h.info)
}

func (h *handler) offer(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxOfferBytes)
	defer request.Body.Close()

	var offer auth.Offer
	if err := json.NewDecoder(request.Body).Decode(&offer); err != nil {
		writeJSONError(writer, &auth.Error{Status: http.StatusBadRequest, Code: "bad-request", Message: "invalid offer JSON"})
		return
	}

	answer, err := h.answerer.Answer(remoteIP(request), offer, nil)
	if err != nil {
		writeJSONError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, answer)
}

// StaticHandler serves the web client and keeps the entry points uncached so
// a phone picks up a new client version immediately.
func StaticHandler(assets fs.FS) http.Handler {
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := request.URL.Path
		if path == "/" || strings.HasSuffix(path, ".html") || strings.HasSuffix(path, "sw.js") || strings.HasSuffix(path, ".webmanifest") {
			writer.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(path, "/assets/") {
			writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(writer, request)
	})
}

// WithCORS allows any origin. Every request that matters is authenticated by
// an HMAC of the access key rather than by cookies or by the origin, so the
// desktop application, a page served by a relay, and a page served by another
// host can all reach this endpoint.
func WithCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Origin") != "" {
			writer.Header().Set("Access-Control-Allow-Origin", "*")
			writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			writer.Header().Set("Access-Control-Max-Age", "600")
		}
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func remoteIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeJSONError(writer http.ResponseWriter, err error) {
	var authError *auth.Error
	if !errors.As(err, &authError) {
		authError = &auth.Error{Status: http.StatusInternalServerError, Code: "internal", Message: err.Error()}
	}
	writeJSON(writer, authError.Status, authError)
}
