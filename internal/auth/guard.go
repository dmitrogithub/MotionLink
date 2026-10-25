package auth

import (
	"strconv"
	"sync"
	"time"
)

// Offer is the signed WebRTC offer sent by a client, either directly to the
// host or through a relay. The relay forwards it without understanding it.
type Offer struct {
	Type      string `json:"type"`
	SDP       string `json:"sdp"`
	Timestamp int64  `json:"ts"`
	Nonce     string `json:"nonce"`
	Quality   string `json:"quality,omitempty"`
	Auth      string `json:"auth"`
}

// Answer is the host's reply. Its signature lets the client verify that the
// answer came from a host that knows the key, not from a relay or a stranger.
type Answer struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
	Auth string `json:"auth"`
}

// Error carries an HTTP-like status so the direct and relay paths report
// authentication problems the same way.
type Error struct {
	Status     int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"error"`
	ServerTime int64  `json:"serverTime,omitempty"`
}

func (e *Error) Error() string { return e.Message }

const (
	clockWindow       = 2 * time.Minute
	failureWindow     = time.Minute
	maxFailures       = 8
	minNonceLength    = 8
	maxNonceLength    = 64
	maxTrackedSources = 4096
)

type failures struct {
	count int
	since time.Time
}

// Guard verifies signed offers for one host key. It rejects replays inside the
// accepted clock window and slows down key guessing per remote address.
type Guard struct {
	key string
	now func() time.Time

	mu       sync.Mutex
	nonces   map[string]time.Time
	failures map[string]*failures
}

func NewGuard(key string) *Guard {
	return &Guard{
		key:      key,
		now:      time.Now,
		nonces:   make(map[string]time.Time),
		failures: make(map[string]*failures),
	}
}

// CheckOffer validates the signature, freshness and uniqueness of an offer.
// source identifies the caller for rate limiting (an IP or "relay").
func (g *Guard) CheckOffer(source string, offer Offer) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	g.pruneLocked(now)

	if entry := g.failures[source]; entry != nil && entry.count >= maxFailures {
		return &Error{Status: 429, Code: "rate-limited", Message: "too many invalid keys, try again in a minute"}
	}
	if len(offer.Nonce) < minNonceLength || len(offer.Nonce) > maxNonceLength {
		return &Error{Status: 400, Code: "bad-request", Message: "the offer nonce is invalid"}
	}

	if !Verify(g.key, offer.Auth, OfferParts(offer)...) {
		g.recordFailureLocked(source, now)
		return &Error{Status: 401, Code: "bad-key", Message: "wrong access key"}
	}

	sent := time.UnixMilli(offer.Timestamp)
	if sent.Before(now.Add(-clockWindow)) || sent.After(now.Add(clockWindow)) {
		return &Error{Status: 401, Code: "clock", Message: "the client clock differs from the host clock", ServerTime: now.UnixMilli()}
	}
	if _, seen := g.nonces[offer.Nonce]; seen {
		return &Error{Status: 401, Code: "replay", Message: "this offer was already used"}
	}
	g.nonces[offer.Nonce] = now
	delete(g.failures, source)
	return nil
}

// SignAnswer signs the host's SDP for the offer it answers.
func (g *Guard) SignAnswer(offer Offer, sdp string) Answer {
	return Answer{Type: "answer", SDP: sdp, Auth: Sign(g.key, AnswerParts(offer.Nonce, sdp)...)}
}

// OfferParts lists the signed fields of an offer in their canonical order.
func OfferParts(offer Offer) []string {
	return []string{"offer", strconv.FormatInt(offer.Timestamp, 10), offer.Nonce, offer.Quality, offer.SDP}
}

// AnswerParts lists the signed fields of an answer in their canonical order.
func AnswerParts(nonce, sdp string) []string {
	return []string{"answer", nonce, sdp}
}

func (g *Guard) recordFailureLocked(source string, now time.Time) {
	entry := g.failures[source]
	if entry == nil {
		if len(g.failures) >= maxTrackedSources {
			return
		}
		entry = &failures{since: now}
		g.failures[source] = entry
	}
	entry.count++
}

func (g *Guard) pruneLocked(now time.Time) {
	for nonce, seen := range g.nonces {
		if now.Sub(seen) > 2*clockWindow {
			delete(g.nonces, nonce)
		}
	}
	for source, entry := range g.failures {
		if now.Sub(entry.since) > failureWindow {
			delete(g.failures, source)
		}
	}
}
