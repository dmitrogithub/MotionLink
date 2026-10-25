package input

// systemInjector is deliberately small so all validation and consent logic can
// be tested without calling Windows APIs.
type systemInjector interface {
	Move(x, y float64) error
	Button(button int, down bool) error
	Wheel(delta int) error
	Key(virtualKey uint16, down bool) error
	// Text types Unicode characters directly, independent of the host's
	// keyboard layout.
	Text(text string) error
}
