# MotionLink Desktop

MotionLink Desktop is the standalone Windows viewer for the MotionLink host. It is a Wails application that renders the incoming WebRTC H.264 stream in its own window.

Build it from this directory:

```powershell
npm install
wails build -m -clean -webview2 browser
```

The executable is written to `build\\bin\\MotionLinkDesktop.exe`.

The application can control a trusted local host after the host is started with `--enable-control` and the user explicitly enables the remote-control checkbox. Mouse and keyboard events are scoped to the video panel; global Windows hooks are not used.

`App.NativeInputStatus` and the `nativeinput` package are safe extension points for future opt-in global Windows event capture.
