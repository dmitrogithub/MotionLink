# MotionLink 2.0

MotionLink streams a Windows desktop with low latency and lets you control it with the mouse and keyboard from a **phone**, **another PC**, or the **desktop app**. FFmpeg captures the screen (NVENC or x264), and Go/Pion delivers it over WebRTC.

```text
Windows PC (host)                                    Client
FFmpeg gdigrab → H.264 RTP → Go/Pion WebRTC ═══════► phone (browser / PWA)
                                │                    another PC (browser)
                                │                    MotionLink Desktop (Wails)
            signaling: direct HTTP or through a relay (+ TURN tunnel)
```

## What's new in 2.0

| Feature | How it works |
|---|---|
| **Phone → PC** | The host serves the web client itself. On the phone, open `http://<PC address>:8080` or scan the QR code in the host window, and the key is filled in for you. Touch control: tap to click, hold to right-click, drag, and use two fingers to scroll. There is also a Trackpad mode, an on-screen keyboard with full Unicode text input, and quick keys (Esc, Tab, Ctrl+C/V, Alt+Tab and more). The client installs as an app (PWA) when it is opened over HTTPS. |
| **PC → PC** | The desktop app and every browser use the same client. The host listens on the network (`0.0.0.0:8080`), not only on `127.0.0.1`. |
| **Access key** | The host shows a key such as `ABCD-EFGH-JKLM`, and the client types it in. The key **never crosses the network**. Offers and answers are signed with HMAC-SHA256, so neither the relay nor anyone else on the network can impersonate the host or the client. Replays and key guessing are blocked. The key persists between runs (`--key` sets your own, `--new-key` generates a new one). |
| **Performance** | Three presets: **30 FPS** (4 Mbit/s, up to 1600 px, for mobile data), **60 FPS** (10 Mbit/s, 1080p) and **High quality** (native resolution, 25 Mbit/s). They switch live, without reconnecting. |
| **Tunneling** | A relay server handles hosts behind NAT: the host connects out to the relay, so no port forwarding is needed. The relay has a built-in TURN server (UDP and TCP) for cases where a direct peer-to-peer path is impossible. On the host, all WebRTC traffic uses **one port** for UDP and TCP (`--ice-port`, 50000 by default), which is the only port to forward. STUN is on by default. Lost packets are retransmitted (NACK). The client reconnects automatically when the network changes (Wi-Fi ↔ LTE), and the host keeps the session for 15 seconds while it recovers. The client shows the connection path: Local network, P2P through NAT, or TURN tunnel. |

Fixed along the way: NVENC never ran before, because the 16×16 probe frame is below the NVENC minimum, so the host always fell back to x264. GPU encoding now works. FFmpeg also no longer keeps running after the host crashes or is killed.

## Requirements

- Host: Windows 10/11 and FFmpeg with `gdigrab` and `h264_nvenc` or `libx264` (in `PATH` or in `components\ffmpeg`)
- Build: Go 1.25+, and Node.js 20+ only to rebuild the web client (a prebuilt copy is in `web/dist`)
- Desktop app: WebView2 Runtime and [Wails v2](https://wails.io)
- Phone client: any modern browser (Chrome, Safari, Firefox)

## Quick start: phone or PC on the same network

```powershell
go run ./cmd/host --enable-control
```

The host window shows the access key, a link such as `http://192.168.1.10:8080/#key=…`, and a QR code. Scan it with the phone or open the link on another PC. When Windows asks for firewall access for the host, allow it on private networks.

Without `--enable-control`, clients can only view. Even with the flag, the client must turn on Control and confirm it.

## Connecting from another network

### Option A: relay server (recommended)

On any VPS with a public IP (Linux works):

```bash
./motionlink-relay-linux-amd64 --listen :8443 --tls-cert cert.pem --tls-key key.pem --public-ip 203.0.113.10
```

Open these ports in the VPS firewall: TCP 8443 (API and web client), UDP+TCP 3478 (STUN/TURN), and UDP 49160–49300 (TURN relay). You can also run the relay behind Caddy or nginx; in that case add `--trust-proxy`.

On the host:

```powershell
go run ./cmd/host --enable-control --relay https://relay.example.com:8443
```

On the phone, open `https://relay.example.com:8443`, choose **Via relay**, and enter the key. Over HTTPS, the client can be added to the home screen as an app.

The relay sees only a hash of the key (the "room" name) and forwards signed messages. It cannot read the video, because DTLS-SRTP encrypts it end to end, and it cannot impersonate the host. TURN credentials last 12 hours and are issued only for a room whose host is online.

### Option B: port forwarding

Forward TCP 8080 and UDP+TCP 50000 on the router to the host PC:

```powershell
go run ./cmd/host --enable-control --public-ip <your public IP>
```

Client: **Direct** mode, address `http://<public IP>:8080`.

## Desktop app

```powershell
cd desktop
wails build -m -clean -webview2 browser
```

`desktop\build\bin\MotionLinkDesktop.exe` uses the same client as the browser: mode, address, key, and quality.

## Host options

| Flag | Default | Purpose |
|---|---|---|
| `--listen` | `0.0.0.0:8080` | Signaling and web client address (`127.0.0.1:8080` limits it to this PC) |
| `--key` / `--new-key` | saved key | Set your own key / generate a new one |
| `--enable-control` | off | Allow remote control |
| `--relay` | — | Relay address for connections from other networks |
| `--ice-port` | `50000` | Single WebRTC port (UDP with TCP fallback); `0` uses random UDP ports |
| `--public-ip` | — | Public IP for a forwarded port |
| `--stun` | Google, Cloudflare | STUN servers; an empty value disables STUN |
| `--turn`, `--turn-user`, `--turn-pass` | — | External TURN server |
| `--rtp-port` | `5004` | Local FFmpeg → host port |
| `--ffmpeg` | auto | Path to `ffmpeg.exe` |
| `--name` | PC name | Name shown to clients |
| `--no-web` | — | Do not serve the web client |

Relay options: `--listen`, `--tls-cert`, `--tls-key`, `--public-ip`, `--turn-port` (`0` disables TURN), `--turn-secret`, `--relay-ports`, `--trust-proxy`, `--no-web`.

## Building and testing

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build.ps1
```

The script builds the web client, checks that the TypeScript and Go cryptography match, runs the tests, and writes `MotionLinkHost.exe`, `MotionLinkRelay.exe`, `motionlink-relay-linux-amd64` (and the desktop app, if Wails is installed) to `dist\`.

Manually:

```powershell
cd web; npm install; npm run build; npm run check:crypto; cd ..
go test ./...
go build ./cmd/host ./cmd/relay
```

## Project layout

```text
cmd/host/            Host: access key, QR banner, network flags
cmd/relay/           Relay server (signaling + TURN)
internal/auth/       Access key, HMAC signatures, replay and brute-force protection
internal/ffmpeg/     FFmpeg/NVENC detection, quality presets, process lifecycle
internal/input/      DataChannel protocol and SendInput (including Unicode text input)
internal/relay/      Relay: rooms, long polling, TURN, and the host-side relay client
internal/rtc/        Pion WebRTC: single ICE port, NACK, reconnection, quality switching
internal/signaling/  Host HTTP signaling and web client serving
web/                 Shared client (phone, browser, desktop), PWA; dist/ is embedded in the host and relay
desktop/             Wails shell around the shared client
```

## Limitations

- Audio is not streamed yet.
- The host runs on Windows only (`gdigrab` capture, `SendInput` input). The relay runs on Windows and Linux.
- `SendInput` cannot control windows running as administrator unless the host also runs elevated, and it cannot press Ctrl+Alt+Del.
- The Windows key is intentionally not forwarded.
- Installing the PWA requires HTTPS (a relay with a certificate). Over `http://`, the phone client works but opens in the browser.

## Security

Every connection is signed with the access key. The host limits wrong attempts (8 per minute per address) and rejects stale (±2 minutes) and repeated requests. The client verifies the answer's signature, so a relay cannot substitute another host. Media and the DataChannel are encrypted with DTLS. Use a long key (`--key`) if the host is reachable from the Internet, and turn on `--enable-control` only when you need remote control.
