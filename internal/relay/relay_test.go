package relay

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"motionlink/internal/auth"
)

// fakeHost answers like signaling.Answerer: it verifies the offer with the
// key and signs the answer.
type fakeHost struct {
	guard *auth.Guard
}

func (h fakeHost) Answer(source string, offer auth.Offer, _ []webrtc.ICEServer) (auth.Answer, error) {
	if err := h.guard.CheckOffer(source, offer); err != nil {
		return auth.Answer{}, err
	}
	return h.guard.SignAnswer(offer, "v=0 answer"), nil
}

func newTestRelay(t *testing.T) *httptest.Server {
	t.Helper()
	server := NewServer(ServerConfig{Name: "test", Logger: log.New(io.Discard, "", 0)})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func postOffer(t *testing.T, base, key string, offer auth.Offer) (int, []byte) {
	t.Helper()
	payload, _ := json.Marshal(offer)
	response, err := http.Post(base+"/api/relay/"+auth.RoomID(key)+"/offer", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, body
}

func signed(key string) auth.Offer {
	offer := auth.Offer{Type: "offer", SDP: "v=0 offer", Timestamp: time.Now().UnixMilli(), Nonce: "nonce-" + time.Now().Format("150405.000000000"), Quality: "fps30"}
	offer.Auth = auth.Sign(key, auth.OfferParts(offer)...)
	return offer
}

func TestOfferToOfflineHostIsRejected(t *testing.T) {
	relay := newTestRelay(t)
	status, body := postOffer(t, relay.URL, "ABCD-EFGH-JKLM", signed("ABCD-EFGH-JKLM"))
	if status != http.StatusNotFound || !strings.Contains(string(body), "host-offline") {
		t.Fatalf("got %d %s, want 404 host-offline", status, body)
	}
}

func TestRelayRoundTrip(t *testing.T) {
	const key = "ABCD-EFGH-JKLM"
	relay := newTestRelay(t)
	client, err := NewClient(relay.URL, key, fakeHost{guard: auth.NewGuard(key)}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)

	// Wait until the host's long poll registers the room.
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := http.Get(relay.URL + "/api/relay/" + auth.RoomID(key) + "/status")
		if err != nil {
			t.Fatal(err)
		}
		var status struct{ Online bool }
		_ = json.NewDecoder(response.Body).Decode(&status)
		response.Body.Close()
		if status.Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host never came online")
		}
		time.Sleep(20 * time.Millisecond)
	}

	offer := signed(key)
	status, body := postOffer(t, relay.URL, key, offer)
	if status != http.StatusOK {
		t.Fatalf("offer through relay: %d %s", status, body)
	}
	var answer auth.Answer
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatal(err)
	}
	if !auth.Verify(key, answer.Auth, auth.AnswerParts(offer.Nonce, answer.SDP)...) {
		t.Fatalf("answer signature does not verify")
	}

	// A wrong key reaches the room only if it hashes to it, so it simply
	// finds no host; a right room with a forged signature is rejected by the
	// host, not the relay.
	forged := signed("WRONG-KEY-123")
	status, body = postOffer(t, relay.URL, key, forged)
	if status != http.StatusUnauthorized || !strings.Contains(string(body), "bad-key") {
		t.Fatalf("forged offer: got %d %s, want 401 bad-key", status, body)
	}
}

func TestInvalidRoomRejected(t *testing.T) {
	relay := newTestRelay(t)
	response, err := http.Get(relay.URL + "/api/relay/not-a-room/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", response.StatusCode)
	}
}
