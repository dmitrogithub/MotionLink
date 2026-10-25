// Package input contains the validated control protocol used by the local
// MotionLink WebRTC DataChannel.
package input

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

const maxMessageBytes = 4096

// maxTextBytes bounds one text event from a phone's on-screen keyboard.
const maxTextBytes = 256

const (
	EventControl = "control"
	EventPointer = "pointer"
	EventButton  = "button"
	EventWheel   = "wheel"
	EventKey     = "key"
	EventText    = "text"
)

// Event is a small, transport-neutral control message. Pointer coordinates are
// normalized to the captured desktop, so the host maps them to its own virtual
// Windows desktop.
type Event struct {
	Type    string  `json:"type"`
	Enabled bool    `json:"enabled,omitempty"`
	X       float64 `json:"x,omitempty"`
	Y       float64 `json:"y,omitempty"`
	Button  int     `json:"button,omitempty"`
	Delta   int     `json:"delta,omitempty"`
	Code    string  `json:"code,omitempty"`
	Down    bool    `json:"down,omitempty"`
	Text    string  `json:"text,omitempty"`
}

// Decode parses one DataChannel message and rejects malformed or unexpected
// input before it reaches any Windows API.
func Decode(payload []byte) (Event, error) {
	if len(payload) == 0 || len(payload) > maxMessageBytes {
		return Event{}, fmt.Errorf("control message size must be between 1 and %d bytes", maxMessageBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var event Event
	if err := decoder.Decode(&event); err != nil {
		return Event{}, fmt.Errorf("decode control message: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Event{}, err
	}
	if err := validate(event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("control message must contain one JSON object")
		}
		return fmt.Errorf("decode trailing control data: %w", err)
	}
	return nil
}

func validate(event Event) error {
	switch event.Type {
	case EventControl:
		return nil
	case EventPointer:
		return validateCoordinates(event)
	case EventButton:
		if event.Button < 0 || event.Button > 2 {
			return fmt.Errorf("unsupported mouse button %d", event.Button)
		}
		return nil
	case EventWheel:
		if event.Delta == 0 || event.Delta < -1200 || event.Delta > 1200 {
			return fmt.Errorf("wheel delta must be between -1200 and 1200, excluding zero")
		}
		return nil
	case EventKey:
		if len(event.Code) == 0 || len(event.Code) > 32 {
			return fmt.Errorf("keyboard code length must be between 1 and 32")
		}
		return nil
	case EventText:
		if len(event.Text) == 0 || len(event.Text) > maxTextBytes || !utf8.ValidString(event.Text) {
			return fmt.Errorf("text must be valid UTF-8 between 1 and %d bytes", maxTextBytes)
		}
		return nil
	default:
		return fmt.Errorf("unsupported control event type %q", event.Type)
	}
}

func validateCoordinates(event Event) error {
	if math.IsNaN(event.X) || math.IsNaN(event.Y) || math.IsInf(event.X, 0) || math.IsInf(event.Y, 0) {
		return fmt.Errorf("pointer coordinates must be finite")
	}
	if event.X < 0 || event.X > 1 || event.Y < 0 || event.Y > 1 {
		return fmt.Errorf("pointer coordinates must be normalized between 0 and 1")
	}
	return nil
}
