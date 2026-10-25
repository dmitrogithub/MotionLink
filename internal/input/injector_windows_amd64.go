//go:build windows && amd64

package input

import (
	"fmt"
	"math"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseMove        = 0x0001
	mouseLeftDown    = 0x0002
	mouseLeftUp      = 0x0004
	mouseRightDown   = 0x0008
	mouseRightUp     = 0x0010
	mouseMiddleDown  = 0x0020
	mouseMiddleUp    = 0x0040
	mouseWheel       = 0x0800
	mouseVirtualDesk = 0x4000
	mouseAbsolute    = 0x8000

	keyUp      = 0x0002
	keyUnicode = 0x0004
)

var (
	user32        = windows.NewLazySystemDLL("user32.dll")
	procSendInput = user32.NewProc("SendInput")
)

// nativeInput is the 64-bit Windows INPUT structure. The explicit padding and
// 32-byte union make unsafe.Sizeof(nativeInput{}) equal to sizeof(INPUT).
type nativeInput struct {
	typeID uint32
	_      uint32
	data   [4]uintptr
}

type mouseInput struct {
	dx        int32
	dy        int32
	mouseData uint32
	flags     uint32
	time      uint32
	extraInfo uintptr
}

type keyboardInput struct {
	virtualKey uint16
	scanCode   uint16
	flags      uint32
	time       uint32
	extraInfo  uintptr
}

type windowsInjector struct{}

func newSystemInjector() systemInjector {
	return windowsInjector{}
}

func (windowsInjector) Move(x, y float64) error {
	return send(nativeMouse(normalize(x), normalize(y), 0, mouseMove|mouseAbsolute|mouseVirtualDesk))
}

func (windowsInjector) Button(button int, down bool) error {
	flags, ok := buttonFlags(button, down)
	if !ok {
		return fmt.Errorf("unsupported mouse button %d", button)
	}
	return send(nativeMouse(0, 0, 0, flags))
}

func (windowsInjector) Wheel(delta int) error {
	return send(nativeMouse(0, 0, uint32(int32(delta)), mouseWheel))
}

func (windowsInjector) Key(virtualKey uint16, down bool) error {
	flags := uint32(0)
	if !down {
		flags = keyUp
	}
	return send(nativeKeyboard(virtualKey, flags))
}

func (windowsInjector) Text(text string) error {
	units := utf16.Encode([]rune(text))
	inputs := make([]nativeInput, 0, len(units)*2)
	for _, unit := range units {
		inputs = append(inputs,
			nativeUnicode(unit, keyUnicode),
			nativeUnicode(unit, keyUnicode|keyUp),
		)
	}
	return send(inputs...)
}

func normalize(value float64) int32 {
	clamped := math.Max(0, math.Min(1, value))
	return int32(math.Round(clamped * 65535))
}

func buttonFlags(button int, down bool) (uint32, bool) {
	switch button {
	case 0:
		if down {
			return mouseLeftDown, true
		}
		return mouseLeftUp, true
	case 1:
		if down {
			return mouseMiddleDown, true
		}
		return mouseMiddleUp, true
	case 2:
		if down {
			return mouseRightDown, true
		}
		return mouseRightUp, true
	default:
		return 0, false
	}
}

func nativeMouse(dx, dy int32, mouseData, flags uint32) nativeInput {
	result := nativeInput{typeID: inputMouse}
	*(*mouseInput)(unsafe.Pointer(&result.data[0])) = mouseInput{
		dx:        dx,
		dy:        dy,
		mouseData: mouseData,
		flags:     flags,
	}
	return result
}

func nativeKeyboard(virtualKey uint16, flags uint32) nativeInput {
	result := nativeInput{typeID: inputKeyboard}
	*(*keyboardInput)(unsafe.Pointer(&result.data[0])) = keyboardInput{
		virtualKey: virtualKey,
		flags:      flags,
	}
	return result
}

func nativeUnicode(unit uint16, flags uint32) nativeInput {
	result := nativeInput{typeID: inputKeyboard}
	*(*keyboardInput)(unsafe.Pointer(&result.data[0])) = keyboardInput{
		scanCode: unit,
		flags:    flags,
	}
	return result
}

func send(inputs ...nativeInput) error {
	if len(inputs) == 0 {
		return nil
	}
	sent, _, _ := procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(nativeInput{}),
	)
	if sent != uintptr(len(inputs)) {
		return fmt.Errorf("SendInput injected %d of %d events", sent, len(inputs))
	}
	return nil
}
