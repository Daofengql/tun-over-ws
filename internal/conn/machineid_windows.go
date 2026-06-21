//go:build windows

package conn

import (
	"golang.org/x/sys/windows/registry"
)

func getMachineID() string {
	return getWindowsMachineID()
}

func getWindowsMachineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.READ)
	if err != nil {
		return "windows-unknown"
	}
	defer k.Close()

	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "windows-unknown"
	}
	return guid
}
