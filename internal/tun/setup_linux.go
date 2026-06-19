package tun

import (
	"fmt"
	"os/exec"
)

func setupIP(name, ip string) error {
	cmd := exec.Command("ip", "addr", "add", ip+"/24", "dev", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip addr add: %w: %s", err, string(out))
	}

	cmd = exec.Command("ip", "link", "set", "dev", name, "up")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up: %w: %s", err, string(out))
	}

	return nil
}

func cleanupIP(name, ip string) {
	_ = exec.Command("ip", "addr", "del", ip+"/24", "dev", name).Run()
	_ = exec.Command("ip", "link", "set", "dev", name, "down").Run()
}
