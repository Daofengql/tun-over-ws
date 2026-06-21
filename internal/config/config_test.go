package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerConfigValidateRejectsIPv6Overlay(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.OverlayCIDR = "fd00::/64"
	cfg.ServerTUN.IP = "fd00::1"

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected IPv6 overlay to be rejected")
	}
}

func TestServerConfigValidateRejectsServerIPOutsideOverlay(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.OverlayCIDR = "10.66.0.0/24"
	cfg.ServerTUN.IP = "10.77.0.1"

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected server_tun.ip outside overlay to be rejected")
	}
}

func TestLoadClientConfigRejectsLegacyUUIDToken(t *testing.T) {
	path := writeTempConfig(t, `server_url: "ws://127.0.0.1:18443/tunnel"
uuid: "legacy-client"
token: "legacy-token"
tun:
  name: "wsvpn0"
  mtu: 1280
`)

	_, err := LoadClientConfig(path)
	if err == nil {
		t.Fatal("expected legacy uuid/token fields to be rejected")
	}
	if !strings.Contains(err.Error(), "field uuid not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadServerConfigRejectsLegacyAuthTokens(t *testing.T) {
	path := writeTempConfig(t, `listen: ":18443"
overlay_cidr: "10.66.0.0/24"
server_tun:
  enabled: false
  name: "wsvpn0"
  ip: "10.66.0.1"
  mtu: 1280
auth:
  tokens:
    - "legacy-token"
`)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected legacy auth.tokens to be rejected")
	}
	if !strings.Contains(err.Error(), "field auth not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
