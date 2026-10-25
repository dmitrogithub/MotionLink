package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/skip2/go-qrcode"

	"motionlink/internal/auth"
)

type savedConfig struct {
	Key string `json:"key"`
}

// resolveKey keeps the access key stable across restarts, so a phone or PC
// that remembered it keeps working. Priority: --key, then the saved key, then
// a new random one.
func resolveKey(flagKey string, regenerate bool) (string, error) {
	path, pathErr := configPath()
	saved := savedConfig{}
	if pathErr == nil && !regenerate {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &saved)
		}
	}

	key := strings.ToUpper(strings.TrimSpace(flagKey))
	switch {
	case key != "":
	case saved.Key != "" && auth.ValidateKey(saved.Key) == nil:
		return saved.Key, nil
	default:
		generated, err := auth.GenerateKey()
		if err != nil {
			return "", err
		}
		key = generated
	}
	if err := auth.ValidateKey(key); err != nil {
		return "", err
	}

	if pathErr == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			data, _ := json.MarshalIndent(savedConfig{Key: key}, "", "  ")
			_ = os.WriteFile(path, data, 0o600)
		}
	}
	return key, nil
}

func configPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "MotionLink", "host.json"), nil
}

// lanLinks lists URLs with the key in the fragment (never sent to a server),
// one per private IPv4 address of this PC.
func lanLinks(listenAddress, key string) []string {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return []string{fmt.Sprintf("http://%s/#key=%s", listenAddress, key)}
	}
	if ip != nil && !ip.IsUnspecified() {
		return []string{fmt.Sprintf("http://%s/#key=%s", net.JoinHostPort(host, port), key)}
	}

	var links []string
	for _, address := range privateIPv4Addresses() {
		links = append(links, fmt.Sprintf("http://%s/#key=%s", net.JoinHostPort(address, port), key))
	}
	return links
}

func privateIPv4Addresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []string
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			prefix, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			if ip := prefix.IP.To4(); ip != nil && ip.IsPrivate() {
				result = append(result, ip.String())
			}
		}
	}
	return result
}

// printQR draws a scannable code in the console so a phone can open the
// client with the key already filled in.
func printQR(content string) {
	code, err := qrcode.New(content, qrcode.Low)
	if err != nil {
		return
	}
	fmt.Println()
	fmt.Println("  Scan with the phone camera:")
	for _, line := range strings.Split(code.ToSmallString(false), "\n") {
		fmt.Println("  " + line)
	}
}
