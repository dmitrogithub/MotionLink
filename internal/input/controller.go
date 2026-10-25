package input

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
)

// Result is returned for control-state changes and sent back to the desktop
// client as an acknowledgement.
type Result struct {
	Enabled bool
	Message string
}

// Controller applies only validated events. It is scoped to a single WebRTC
// client session and tracks pressed input so it can safely release it on close.
type Controller struct {
	allowed bool
	logger  *log.Logger
	inject  systemInjector

	mu             sync.Mutex
	enabled        bool
	pressedKeys    map[uint16]struct{}
	pressedButtons map[int]struct{}
}

func NewController(allowed bool, logger *log.Logger) *Controller {
	return &Controller{
		allowed:        allowed,
		logger:         logger,
		inject:         newSystemInjector(),
		pressedKeys:    make(map[uint16]struct{}),
		pressedButtons: make(map[int]struct{}),
	}
}

// Handle applies one event. Non-control events are ignored until both the host
// flag and the desktop client's explicit consent have enabled the controller.
func (c *Controller) Handle(event Event) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if event.Type == EventControl {
		return c.setEnabledLocked(event.Enabled)
	}
	if !c.enabled {
		return Result{}, nil
	}

	switch event.Type {
	case EventPointer:
		return Result{}, c.inject.Move(event.X, event.Y)
	case EventButton:
		return Result{}, c.buttonLocked(event.Button, event.Down)
	case EventWheel:
		return Result{}, c.inject.Wheel(event.Delta)
	case EventKey:
		return Result{}, c.keyLocked(event.Code, event.Down)
	case EventText:
		return Result{}, c.inject.Text(event.Text)
	default:
		return Result{}, fmt.Errorf("unsupported control event type %q", event.Type)
	}
}

// ReleaseAll releases every button and key recorded for this session. It is
// called as the peer connection closes to avoid leaving the host's input stuck.
func (c *Controller) ReleaseAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = false
	return c.releaseAllLocked()
}

func (c *Controller) setEnabledLocked(enabled bool) (Result, error) {
	if enabled && !c.allowed {
		c.enabled = false
		return Result{
			Enabled: false,
			Message: "Host control is disabled. Restart the host with --enable-control.",
		}, nil
	}

	if !enabled {
		err := c.releaseAllLocked()
		c.enabled = false
		c.logf("remote control disabled")
		return Result{Enabled: false, Message: "Remote control disabled."}, err
	}

	c.enabled = true
	c.logf("remote control enabled")
	return Result{Enabled: true, Message: "Remote control enabled. Click the video before using the keyboard."}, nil
}

func (c *Controller) buttonLocked(button int, down bool) error {
	if down {
		if _, pressed := c.pressedButtons[button]; pressed {
			return nil
		}
		if err := c.inject.Button(button, true); err != nil {
			return err
		}
		c.pressedButtons[button] = struct{}{}
		return nil
	}

	if err := c.inject.Button(button, false); err != nil {
		return err
	}
	delete(c.pressedButtons, button)
	return nil
}

func (c *Controller) keyLocked(code string, down bool) error {
	virtualKey, ok := virtualKeyForCode(code)
	if !ok {
		return fmt.Errorf("unsupported keyboard code %q", code)
	}

	if down {
		if _, pressed := c.pressedKeys[virtualKey]; pressed {
			return nil
		}
		if err := c.inject.Key(virtualKey, true); err != nil {
			return err
		}
		c.pressedKeys[virtualKey] = struct{}{}
		return nil
	}

	if err := c.inject.Key(virtualKey, false); err != nil {
		return err
	}
	delete(c.pressedKeys, virtualKey)
	return nil
}

func (c *Controller) releaseAllLocked() error {
	var firstError error
	for virtualKey := range c.pressedKeys {
		if err := c.inject.Key(virtualKey, false); err != nil && firstError == nil {
			firstError = err
		}
		delete(c.pressedKeys, virtualKey)
	}
	for button := range c.pressedButtons {
		if err := c.inject.Button(button, false); err != nil && firstError == nil {
			firstError = err
		}
		delete(c.pressedButtons, button)
	}
	return firstError
}

func (c *Controller) logf(format string, arguments ...any) {
	if c.logger != nil {
		c.logger.Printf(format, arguments...)
	}
}

func virtualKeyForCode(code string) (uint16, bool) {
	if len(code) == 4 && strings.HasPrefix(code, "Key") {
		letter := code[3]
		if letter >= 'A' && letter <= 'Z' {
			return uint16(letter), true
		}
	}
	if len(code) == 6 && strings.HasPrefix(code, "Digit") {
		digit := code[5]
		if digit >= '0' && digit <= '9' {
			return uint16(digit), true
		}
	}
	if len(code) == 7 && strings.HasPrefix(code, "Numpad") {
		digit := code[6]
		if digit >= '0' && digit <= '9' {
			return 0x60 + uint16(digit-'0'), true
		}
	}
	if len(code) >= 2 && code[0] == 'F' {
		if number, err := strconv.Atoi(code[1:]); err == nil && number >= 1 && number <= 24 {
			return 0x70 + uint16(number-1), true
		}
	}

	virtualKeys := map[string]uint16{
		"Backspace":       0x08,
		"Tab":             0x09,
		"Enter":           0x0D,
		"ShiftLeft":       0xA0,
		"ShiftRight":      0xA1,
		"ControlLeft":     0xA2,
		"ControlRight":    0xA3,
		"AltLeft":         0xA4,
		"AltRight":        0xA5,
		"Pause":           0x13,
		"CapsLock":        0x14,
		"Escape":          0x1B,
		"Space":           0x20,
		"PageUp":          0x21,
		"PageDown":        0x22,
		"End":             0x23,
		"Home":            0x24,
		"ArrowLeft":       0x25,
		"ArrowUp":         0x26,
		"ArrowRight":      0x27,
		"ArrowDown":       0x28,
		"Insert":          0x2D,
		"Delete":          0x2E,
		"NumLock":         0x90,
		"ScrollLock":      0x91,
		"NumpadMultiply":  0x6A,
		"NumpadAdd":       0x6B,
		"NumpadSubtract":  0x6D,
		"NumpadDecimal":   0x6E,
		"NumpadDivide":    0x6F,
		"Semicolon":       0xBA,
		"Equal":           0xBB,
		"Comma":           0xBC,
		"Minus":           0xBD,
		"Period":          0xBE,
		"Slash":           0xBF,
		"Backquote":       0xC0,
		"BracketLeft":     0xDB,
		"Backslash":       0xDC,
		"BracketRight":    0xDD,
		"Quote":           0xDE,
		"IntlBackslash":   0xE2,
		"ContextMenu":     0x5D,
		"PrintScreen":     0x2C,
		"NumpadEnter":     0x0D,
		"AudioVolumeUp":   0xAF,
		"AudioVolumeDown": 0xAE,
		"AudioVolumeMute": 0xAD,
		"MediaPlayPause":  0xB3,
	}
	virtualKey, ok := virtualKeys[code]
	return virtualKey, ok
}
