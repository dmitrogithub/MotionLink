// Package auth implements the MotionLink access key: a short human-readable
// secret that the host shows and the client types in. The key itself never
// crosses the network; both sides prove knowledge of it with HMAC-SHA256
// signatures over the SDP, which also binds the DTLS fingerprints to the key
// and prevents a relay from substituting its own peer.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// keyAlphabet omits characters that are easy to confuse when typing a key
// from a phone screen (0/O, 1/I/L).
const keyAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

const (
	keyGroups     = 3
	keyGroupSize  = 4
	minKeyLength  = 6
	maxKeyLength  = 64
	roomIDBytes   = 16
	roomIDContext = "motionlink-room:"
)

// GenerateKey returns a random key in the form XXXX-XXXX-XXXX (~59 bits).
func GenerateKey() (string, error) {
	random := make([]byte, keyGroups*keyGroupSize)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	var builder strings.Builder
	for index, value := range random {
		if index > 0 && index%keyGroupSize == 0 {
			builder.WriteByte('-')
		}
		// 256 % 31 introduces a negligible bias for a human-entered key.
		builder.WriteByte(keyAlphabet[int(value)%len(keyAlphabet)])
	}
	return builder.String(), nil
}

// NormalizeKey makes typed keys comparable: case, spaces and dashes are
// ignored so "abcd-efgh" and "ABCD EFGH" are the same key.
func NormalizeKey(key string) string {
	var builder strings.Builder
	for _, character := range strings.ToUpper(key) {
		if character == '-' || character == ' ' || character == '\t' {
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

// ValidateKey rejects keys that are too short to be meaningful.
func ValidateKey(key string) error {
	normalized := NormalizeKey(key)
	if len(normalized) < minKeyLength || len(normalized) > maxKeyLength {
		return errors.New("the access key must contain 6 to 64 characters")
	}
	return nil
}

// RoomID derives the public relay room name from a key. The relay only sees
// this hash, so it cannot recover the key or sign offers.
func RoomID(key string) string {
	sum := sha256.Sum256([]byte(roomIDContext + NormalizeKey(key)))
	return hex.EncodeToString(sum[:roomIDBytes])
}

// Sign returns hex(HMAC-SHA256(key, parts joined by '\n')).
func Sign(key string, parts ...string) string {
	mac := hmac.New(sha256.New, []byte(NormalizeKey(key)))
	mac.Write([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify compares a signature in constant time.
func Verify(key, signature string, parts ...string) bool {
	expected := Sign(key, parts...)
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(signature)))
}
