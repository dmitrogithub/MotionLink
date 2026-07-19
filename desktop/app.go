package main

import (
	"context"

	"motionlink-desktop/nativeinput"
)

// App owns the native side of the desktop client. Browser/WebView code never
// needs direct Win32 access: future input capture is exposed from this bridge.
type App struct {
	ctx   context.Context
	input nativeinput.Source
}

// NativeInputStatus is deliberately small and stable so the UI can display the
// current capability without relying on browser permissions. Windows hooks are
// not enabled in this video-only iteration.
type NativeInputStatus struct {
	Available bool   `json:"available"`
	State     string `json:"state"`
	Detail    string `json:"detail"`
}

// NewApp creates the application bridge.
func NewApp() *App {
	return &App{input: nativeinput.NewReservedSource()}
}

// startup stores the Wails lifecycle context for future native integrations.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// NativeInputStatus reports the reserved native input bridge. The next stage
// will implement a Windows-only InputSource using system hooks and send
// explicitly enabled events over a dedicated WebRTC DataChannel.
func (a *App) NativeInputStatus() NativeInputStatus {
	status := a.input.Status()
	return NativeInputStatus{
		Available: status.Available,
		State:     status.State,
		Detail:    status.Detail,
	}
}
