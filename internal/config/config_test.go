package config

import "testing"

func TestServerConfigValidateRejectsIPv6Overlay(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.OverlayCIDR = "fd00::/64"
	cfg.ServerTUN.IP = "fd00::1"
	cfg.Auth.Tokens = []string{"test-token"}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected IPv6 overlay to be rejected")
	}
}

func TestServerConfigValidateRejectsServerIPOutsideOverlay(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.OverlayCIDR = "10.66.0.0/24"
	cfg.ServerTUN.IP = "10.77.0.1"
	cfg.Auth.Tokens = []string{"test-token"}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected server_tun.ip outside overlay to be rejected")
	}
}
