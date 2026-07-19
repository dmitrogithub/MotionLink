# MotionLink Desktop

MotionLink Desktop is the standalone Windows viewer for the MotionLink host. It is a Wails application that renders the incoming WebRTC H.264 stream in its own window.

Build it from this directory:

```powershell
npm install
wails build -m -clean -webview2 browser
```

The executable is written to `build\\bin\\MotionLinkDesktop.exe`.

`App.NativeInputStatus` and the `nativeinput` package are safe extension points for future Windows event capture. Native input collection is disabled in the current MVP.
