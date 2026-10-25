//go:build !windows || !amd64

package input

import "errors"

type unsupportedInjector struct{}

func newSystemInjector() systemInjector {
	return unsupportedInjector{}
}

func (unsupportedInjector) Move(float64, float64) error { return unsupportedError() }
func (unsupportedInjector) Button(int, bool) error      { return unsupportedError() }
func (unsupportedInjector) Wheel(int) error             { return unsupportedError() }
func (unsupportedInjector) Key(uint16, bool) error      { return unsupportedError() }
func (unsupportedInjector) Text(string) error           { return unsupportedError() }

func unsupportedError() error {
	return errors.New("remote input injection is supported only on Windows amd64")
}
