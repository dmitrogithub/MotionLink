# Builds everything into .\dist: the host, the relay (Windows and Linux) and,
# when the Wails CLI is installed, the desktop application.
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
New-Item -ItemType Directory -Force dist | Out-Null

Push-Location web
npm install --no-audit --no-fund
npm run build
npm run check:crypto
Pop-Location

go test ./...
go build -trimpath -ldflags "-s -w" -o dist\MotionLinkHost.exe ./cmd/host
go build -trimpath -ldflags "-s -w" -o dist\MotionLinkRelay.exe ./cmd/relay
$env:GOOS = "linux"; $env:GOARCH = "amd64"
go build -trimpath -ldflags "-s -w" -o dist\motionlink-relay-linux-amd64 ./cmd/relay
Remove-Item Env:GOOS, Env:GOARCH

if (Get-Command wails -ErrorAction SilentlyContinue) {
    Push-Location desktop
    wails build -m -clean -webview2 browser
    Copy-Item build\bin\MotionLinkDesktop.exe ..\dist\ -Force
    Pop-Location
} else {
    Write-Host "Wails CLI not found: skipping the desktop app (go install github.com/wailsapp/wails/v2/cmd/wails@latest)"
}
Write-Host "Done: $root\dist"
