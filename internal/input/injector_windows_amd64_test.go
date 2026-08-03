//go:build windows && amd64

package input

import (
	"testing"
	"unsafe"
)

func TestNativeInputLayout(t *testing.T) {
	if got := unsafe.Sizeof(nativeInput{}); got != 40 {
		t.Fatalf("INPUT layout is %d bytes, want 40", got)
	}
	if got := unsafe.Sizeof(mouseInput{}); got != 32 {
		t.Fatalf("MOUSEINPUT layout is %d bytes, want 32", got)
	}
}
