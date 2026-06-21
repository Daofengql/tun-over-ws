package conn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"runtime"
)

// DeviceInfo contains device identification information.
type DeviceInfo struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Hostname  string `json:"hostname"`
	MachineID string `json:"machine_id"`
}

// CollectDeviceInfo gathers device information for identification.
func CollectDeviceInfo() DeviceInfo {
	hostname, _ := os.Hostname()
	return DeviceInfo{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Hostname:  hostname,
		MachineID: getMachineID(),
	}
}

// ToJSON serializes DeviceInfo to JSON string.
func (d DeviceInfo) ToJSON() string {
	b, _ := json.Marshal(d)
	return string(b)
}

// StableDeviceID derives the device identity from platform machine identity.
func (d DeviceInfo) StableDeviceID() string {
	source := d.OS + "|" + d.Arch + "|" + d.MachineID
	h := sha256.Sum256([]byte(source))
	return "dev-" + hex.EncodeToString(h[:16])
}
