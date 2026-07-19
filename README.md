# MotionLink

MotionLink is an experimental, Windows-only local desktop streaming MVP. It captures the Windows desktop with FFmpeg, relays H.264 RTP through Go/Pion WebRTC, and displays the stream in a standalone Windows application.

The native viewer is built with Wails. It uses Windows WebView2 as its rendering and WebRTC engine, but it runs in its own application window — not in a browser tab.

## Architecture

```text
Windows desktop
  -> FFmpeg (gdigrab + H.264)
  -> RTP on 127.0.0.1:5004
  -> Go / Pion / TrackLocalStaticRTP
  -> WebRTC
  -> MotionLink Desktop (Wails application)
```

## Current capabilities

- Streams the full Windows desktop to one local desktop client.
- Uses HTTP offer/answer signaling at `POST /api/offer`.
- Uses low-latency capture settings: 60 FPS, short GOP, no B-frames, and RTP packets up to 1200 bytes.
- Checks whether `h264_nvenc` works at runtime and falls back to `libx264` when it does not.
- Starts FFmpeg only after the WebRTC connection is established and stops it when the client disconnects.
- Provides a standalone desktop viewer: `MotionLinkDesktop.exe`.
- Defines a native input boundary for a future Windows event-capture implementation.

## Not implemented yet

- Audio
- Remote mouse and keyboard control
- Windows input hooks
- MediaPipe or hand tracking
- Authentication
- STUN/TURN, LAN discovery, or Internet connectivity
- Multi-client streaming

The current host binds to `127.0.0.1`, so the MVP is intended to run on one PC only.

## Native input roadmap

MotionLink Desktop already contains the Go-level [`nativeinput.Source`](desktop/nativeinput/source.go) contract. It is intentionally inactive in this release: the application does not observe, transmit, or inject user input.

The next input stage should follow this explicit flow:

```text
User explicitly enables input capture
  -> Windows hooks in the Wails/Go client
  -> a dedicated WebRTC DataChannel
  -> validation on the Go host
  -> SendInput on the host
```

This separation keeps privileged OS event access outside frontend JavaScript and makes the consent boundary visible.

## Prerequisites

- Windows 10 or 11
- Go 1.24 or newer
- Node.js 20 or newer with npm
- FFmpeg with `gdigrab` and either `h264_nvenc` or `libx264`
- Microsoft Edge WebView2 Runtime
- Wails v2 for building the desktop application

Install Wails after Go is available:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

The project also supports local tool copies under `components/`, but that directory is intentionally ignored by Git.

## Quick start

Install the dependencies:

```powershell
go mod download

cd desktop
go mod download
npm install
cd ..
```

Start the local host in one PowerShell window:

```powershell
go run -buildvcs=false ./cmd/host
```

Build the desktop application in another window:

```powershell
cd desktop
wails build -m -clean -webview2 browser
```

Run `desktop\build\bin\MotionLinkDesktop.exe`, keep the default host address `http://127.0.0.1:8080`, and select **Connect**.

Expected host output:

```text
WebRTC connection state: connected
FFmpeg started: encoder=h264_nvenc RTP=127.0.0.1:5004
desktop RTP is flowing: first H.264 packet received from FFmpeg
```

Seeing `libx264` instead of `h264_nvenc` is valid: FFmpeg is using the CPU encoder.

## Configuration

By default, the host uses HTTP signaling on `127.0.0.1:8080` and receives RTP on `127.0.0.1:5004`.

Use a different RTP port:

```powershell
go run -buildvcs=false ./cmd/host --rtp-port 5006
```

Use a specific FFmpeg executable:

```powershell
go run -buildvcs=false ./cmd/host --ffmpeg "C:\Tools\ffmpeg\bin\ffmpeg.exe"
```

## Validation

Host:

```powershell
go test -buildvcs=false ./...
go build -buildvcs=false ./cmd/host
```

Desktop application:

```powershell
cd desktop
go test -buildvcs=false ./...
npm run build
wails build -m -clean -webview2 browser
```

## Project layout

```text
cmd/host/              Local host entry point
internal/ffmpeg/       FFmpeg discovery, encoder selection, and capture lifecycle
internal/rtc/          Pion WebRTC and RTP -> TrackLocalStaticRTP relay
internal/signaling/    HTTP offer/answer signaling and CORS
desktop/               Wails desktop application
desktop/frontend/      Strict TypeScript desktop UI
desktop/nativeinput/   Contract for future native Windows input capture
web/                   Legacy browser prototype (not the primary client)
```

## Troubleshooting

- If the application connects but no video appears, check the host terminal for `desktop RTP is flowing`.
- If that line is absent, FFmpeg is not producing RTP packets. Confirm that your FFmpeg build supports `gdigrab` and `libx264` or `h264_nvenc`.
- If `h264_nvenc` is listed but cannot initialize, MotionLink automatically switches to `libx264`.
- If the video requires user interaction, select **Start video** in the desktop client.

## Security note

MotionLink is a local prototype, not a hardened remote-access product. Do not expose its HTTP signaling endpoint to untrusted networks. Authentication, encrypted peer discovery, consent UI, and input authorization must be added before any Internet or remote-control use.
