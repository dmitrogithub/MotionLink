package auth

import (
	"errors"
	"testing"
	"time"
)

func TestGenerateKeyFormat(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 14 || key[4] != '-' || key[9] != '-' {
		t.Fatalf("unexpected key format %q", key)
	}
	if err := ValidateKey(key); err != nil {
		t.Fatalf("generated key is invalid: %v", err)
	}
}

func TestNormalizeKeyAndRoom(t *testing.T) {
	if NormalizeKey(" ab-cd efgh ") != "ABCDEFGH" {
		t.Fatalf("NormalizeKey did not strip separators")
	}
	if RoomID("abcd-efgh") != RoomID("ABCDEFGH") {
		t.Fatalf("room IDs differ for equivalent keys")
	}
	if len(RoomID("ABCDEFGH")) != 32 {
		t.Fatalf("room ID must be 32 hex characters")
	}
}

// Known vectors shared with web/scripts/crypto-check.mjs so the TypeScript
// and Go implementations cannot drift apart.
func TestSignKnownVector(t *testing.T) {
	got := Sign("abcd-efgh", "offer", "1700000000000", "nonce123", "fps60", "v=0")
	if want := "e7c265a782bf05951ba7a9906131f024d1c0afa68bb39d977d92c16e76cf4d8a"; got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
	if got, want := RoomID("abcd-efgh"), "7e20519067b634b079e5e32b79cc8f63"; got != want {
		t.Fatalf("RoomID = %s, want %s", got, want)
	}
}

func signedOffer(key string, at time.Time, nonce string) Offer {
	offer := Offer{Type: "offer", SDP: "v=0", Timestamp: at.UnixMilli(), Nonce: nonce, Quality: "fps60"}
	offer.Auth = Sign(key, OfferParts(offer)...)
	return offer
}

func TestGuardAcceptsValidOfferOnce(t *testing.T) {
	guard := NewGuard("ABCD-EFGH-JKLM")
	offer := signedOffer("abcdefghjklm", time.Now(), "nonce-0001")
	if err := guard.CheckOffer("a", offer); err != nil {
		t.Fatalf("valid offer rejected: %v", err)
	}
	var authError *Error
	if err := guard.CheckOffer("a", offer); !errors.As(err, &authError) || authError.Code != "replay" {
		t.Fatalf("replayed offer was not rejected: %v", err)
	}
}

func TestGuardRejectsWrongKeyAndRateLimits(t *testing.T) {
	guard := NewGuard("ABCD-EFGH-JKLM")
	var authError *Error
	for attempt := 0; attempt < maxFailures; attempt++ {
		err := guard.CheckOffer("attacker", signedOffer("WRONG-KEY", time.Now(), "nonce-x"+string(rune('a'+attempt))))
		if !errors.As(err, &authError) || authError.Code != "bad-key" {
			t.Fatalf("attempt %d: expected bad-key, got %v", attempt, err)
		}
	}
	err := guard.CheckOffer("attacker", signedOffer("ABCD-EFGH-JKLM", time.Now(), "nonce-final"))
	if !errors.As(err, &authError) || authError.Code != "rate-limited" {
		t.Fatalf("expected rate limit, got %v", err)
	}
}

func TestGuardRejectsStaleClock(t *testing.T) {
	guard := NewGuard("ABCD-EFGH-JKLM")
	var authError *Error
	err := guard.CheckOffer("a", signedOffer("ABCD-EFGH-JKLM", time.Now().Add(-10*time.Minute), "nonce-old1"))
	if !errors.As(err, &authError) || authError.Code != "clock" || authError.ServerTime == 0 {
		t.Fatalf("expected clock error with server time, got %v", err)
	}
}

func TestAnswerSignature(t *testing.T) {
	guard := NewGuard("ABCD-EFGH-JKLM")
	offer := signedOffer("ABCD-EFGH-JKLM", time.Now(), "nonce-ans1")
	answer := guard.SignAnswer(offer, "v=0 answer")
	if !Verify("abcd efgh jklm", answer.Auth, AnswerParts(offer.Nonce, answer.SDP)...) {
		t.Fatalf("answer signature does not verify")
	}
}
