// Package nativeinput defines the native input boundary used by the desktop
// application. A Windows implementation will use this contract for global
// hooks; it must only be started after an explicit user action.
package nativeinput

import (
	"context"
	"errors"
)

// EventType describes a future native mouse or keyboard event.
type EventType string

const (
	MouseMove   EventType = "mouse_move"
	MouseButton EventType = "mouse_button"
	MouseWheel  EventType = "mouse_wheel"
	Key         EventType = "key"
)

// Event is transport-neutral. A future WebRTC DataChannel adapter will encode
// only explicitly enabled events after validating their source and timestamp.
type Event struct {
	Type EventType
	X    int
	Y    int
	Key  string
	Down bool
}

// Status describes whether native event collection is enabled for the current
// client session.
type Status struct {
	Available bool
	State     string
	Detail    string
}

// Source is intentionally independent from Wails and WebRTC. This keeps the
// Windows hook implementation testable and lets the app decide when events may
// be sent to a remote host.
type Source interface {
	Start(context.Context, func(Event)) error
	Stop()
	Status() Status
}

// ReservedSource is the safe MVP implementation. It documents the intended
// integration point while guaranteeing that no global keyboard or mouse events
// are observed in the current release.
type ReservedSource struct{}

func NewReservedSource() *ReservedSource {
	return &ReservedSource{}
}

func (s *ReservedSource) Start(context.Context, func(Event)) error {
	return errors.New("native input capture is not implemented")
}

func (s *ReservedSource) Stop() {}

func (s *ReservedSource) Status() Status {
	return Status{
		Available: false,
		State:     "reserved",
		Detail:    "Native input is reserved for a future release; mouse and keyboard capture are currently disabled.",
	}
}
