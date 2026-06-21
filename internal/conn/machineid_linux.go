//go:build linux

package conn

import (
	"os"
	"strings"
)

func getMachineID() string {
	return getLinuxMachineID()
}

func getLinuxMachineID() string {
	data, err := os.ReadFile("/etc/machine-id")
	if err == nil {
		return strings.TrimSpace(string(data))
	}

	data, err = os.ReadFile("/var/lib/dbus/machine-id")
	if err == nil {
		return strings.TrimSpace(string(data))
	}

	return "linux-unknown"
}
