package tun

import (
	"fmt"
	"os/exec"
	"strings"
)

func setupIP(name, ip string) error {
	// netsh interface ip set address "name" static ip mask
	cmd := exec.Command("netsh", "interface", "ip", "set", "address",
		"name="+name, "static", ip, "255.255.255.0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh set address: %w: %s", err, string(out))
	}
	return nil
}

func cleanupIP(name, ip string) {
	// Remove the connected route added by setupIP.
	// Ignore errors - best effort cleanup.
	mask := "255.255.255.0"
	cmd := exec.Command("route", "delete", ip+"/24")
	cmd.Run()
	_ = mask
	_ = strings.Contains
}
