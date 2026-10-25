package input

import "testing"

func TestDecodeAcceptsPointerEvent(t *testing.T) {
	event, err := Decode([]byte(`{"type":"pointer","x":0.25,"y":0.75}`))
	if err != nil {
		t.Fatalf("Decode returned an error: %v", err)
	}
	if event.Type != EventPointer || event.X != 0.25 || event.Y != 0.75 {
		t.Fatalf("Decode returned %+v, want normalized pointer event", event)
	}
}

func TestDecodeRejectsInvalidControlMessages(t *testing.T) {
	testCases := []string{
		`{"type":"pointer","x":1.1,"y":0}`,
		`{"type":"wheel","delta":0}`,
		`{"type":"key","code":""}`,
		`{"type":"unknown"}`,
		`{"type":"pointer","x":0,"y":0,"unexpected":true}`,
		`{"type":"text","text":""}`,
	}

	for _, payload := range testCases {
		t.Run(payload, func(t *testing.T) {
			if _, err := Decode([]byte(payload)); err == nil {
				t.Fatalf("Decode(%s) succeeded, want validation error", payload)
			}
		})
	}
}

func TestDecodeAcceptsUnicodeText(t *testing.T) {
	event, err := Decode([]byte(`{"type":"text","text":"Grüße 👋 日本"}`))
	if err != nil {
		t.Fatalf("Decode returned an error: %v", err)
	}
	if event.Type != EventText || event.Text != "Grüße 👋 日本" {
		t.Fatalf("Decode returned %+v", event)
	}
}
