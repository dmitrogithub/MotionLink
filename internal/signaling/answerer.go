package signaling

import (
	"errors"
	"log"
	"strings"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/auth"
	"motionlink/internal/rtc"
)

const maxOfferBytes = 1 << 20

// Answerer turns signed offers into signed answers. The direct HTTP endpoint
// and the relay client share it, so both paths enforce the same key checks.
type Answerer struct {
	guard     *auth.Guard
	rtcServer *rtc.Server
	logger    *log.Logger
}

func NewAnswerer(key string, rtcServer *rtc.Server, logger *log.Logger) *Answerer {
	return &Answerer{guard: auth.NewGuard(key), rtcServer: rtcServer, logger: logger}
}

// Answer verifies the offer and returns the host's signed answer. Errors are
// always *auth.Error so callers can map them to an HTTP status.
func (a *Answerer) Answer(source string, offer auth.Offer, extraICEServers []webrtc.ICEServer) (auth.Answer, error) {
	if offer.Type != "offer" || strings.TrimSpace(offer.SDP) == "" {
		return auth.Answer{}, &auth.Error{Status: 400, Code: "bad-request", Message: "an SDP offer is required"}
	}
	if err := a.guard.CheckOffer(source, offer); err != nil {
		var authError *auth.Error
		if errors.As(err, &authError) && authError.Code == "bad-key" {
			a.logger.Printf("rejected an offer with a wrong access key from %s", source)
		}
		return auth.Answer{}, err
	}

	answer, err := a.rtcServer.AcceptOffer(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offer.SDP,
	}, offer.Quality, extraICEServers)
	if err != nil {
		a.logger.Printf("offer rejected: %v", err)
		return auth.Answer{}, &auth.Error{Status: 500, Code: "webrtc", Message: "could not create WebRTC answer: " + err.Error()}
	}
	a.logger.Printf("authenticated client from %s (quality %s)", source, qualityOrDefault(offer.Quality))
	return a.guard.SignAnswer(offer, answer.SDP), nil
}

func qualityOrDefault(quality string) string {
	if quality == "" {
		return "default"
	}
	return quality
}
