package signaling

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/rtc"
)

const maxOfferBytes = 1 << 20

type offerPayload struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type answerPayload struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

// NewHTTPServer creates the local HTTP endpoint used for non-trickle
// offer/answer signaling. It deliberately does not use a package-global mux.
func NewHTTPServer(address string, rtcServer *rtc.Server, logger *log.Logger) *http.Server {
	handler := &handler{rtcServer: rtcServer, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.HandleFunc("POST /api/offer", handler.offer)

	return &http.Server{
		Addr:    address,
		Handler: withCORS(mux),
	}
}

type handler struct {
	rtcServer *rtc.Server
	logger    *log.Logger
}

func (h *handler) health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"ok":true}`))
}

func (h *handler) offer(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxOfferBytes)
	defer request.Body.Close()

	var payload offerPayload
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeJSONError(writer, http.StatusBadRequest, "invalid offer JSON")
		return
	}
	if payload.Type != "offer" || strings.TrimSpace(payload.SDP) == "" {
		writeJSONError(writer, http.StatusBadRequest, "an SDP offer is required")
		return
	}

	answer, err := h.rtcServer.AcceptOffer(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  payload.SDP,
	})
	if err != nil {
		var busyError rtc.ClientAlreadyConnectedError
		if errors.As(err, &busyError) {
			writeJSONError(writer, http.StatusConflict, err.Error())
			return
		}
		h.logger.Printf("offer rejected: %v", err)
		writeJSONError(writer, http.StatusInternalServerError, "could not create WebRTC answer")
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(answerPayload{Type: answer.Type.String(), SDP: answer.SDP})
}

func withCORS(next http.Handler) http.Handler {
	allowedOrigins := map[string]struct{}{
		"http://127.0.0.1:5173": {},
		"http://localhost:5173": {},
		// Wails uses an embedded WebView2 window instead of a browser tab.
		// The exact origin differs between Wails runtimes, so accept both
		// supported Windows values while the signaling server itself remains
		// bound to localhost.
		"http://wails.localhost":  {},
		"https://wails.localhost": {},
		"wails://wails":           {},
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		if _, allowed := allowedOrigins[origin]; allowed {
			writer.Header().Set("Access-Control-Allow-Origin", origin)
			writer.Header().Set("Vary", "Origin")
			writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func writeJSONError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": message})
}
