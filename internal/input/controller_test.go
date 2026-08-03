package input

import "testing"

func TestControllerRejectsEnableWithoutHostFlag(t *testing.T) {
	controller := NewController(false, nil)
	result, err := controller.Handle(Event{Type: EventControl, Enabled: true})
	if err != nil {
		t.Fatalf("Handle returned an error: %v", err)
	}
	if result.Enabled {
		t.Fatalf("remote control was enabled without the host flag")
	}
	if result.Message == "" {
		t.Fatalf("expected an explanatory rejection message")
	}
}

func TestVirtualKeyForCode(t *testing.T) {
	testCases := map[string]uint16{
		"KeyA":         'A',
		"Digit9":       '9',
		"Numpad3":      0x63,
		"ArrowLeft":    0x25,
		"ControlRight": 0xA3,
		"F12":          0x7B,
	}

	for code, want := range testCases {
		t.Run(code, func(t *testing.T) {
			got, ok := virtualKeyForCode(code)
			if !ok || got != want {
				t.Fatalf("virtualKeyForCode(%q) = (%#x, %t), want (%#x, true)", code, got, ok, want)
			}
		})
	}

	if _, ok := virtualKeyForCode("MetaLeft"); ok {
		t.Fatalf("Windows logo key must not be forwarded")
	}
}
