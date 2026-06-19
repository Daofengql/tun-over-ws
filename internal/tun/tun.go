package tun

import (
	"fmt"

	"golang.zx2c4.com/wireguard/tun"
)

// DefaultMTU is the default MTU for TUN devices.
const DefaultMTU = 1280

// Device wraps a wireguard-go TUN device with IP configuration.
type Device struct {
	dev  tun.Device
	name string
}

// Create creates a new TUN device with the given name and MTU.
func Create(name string, mtu int) (*Device, error) {
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	dev, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, fmt.Errorf("create tun: %w", err)
	}
	actualName, err := dev.Name()
	if err != nil {
		actualName = name
	}
	return &Device{dev: dev, name: actualName}, nil
}

// Name returns the TUN interface name.
func (d *Device) Name() string { return d.name }

// Read reads a packet from the TUN. Returns raw IPv4 bytes (family prefix stripped).
func (d *Device) Read(buf []byte) (int, error) {
	bufs := [][]byte{buf}
	sizes := []int{0}
	n, err := d.dev.Read(bufs, sizes, 0)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	return sizes[0], nil
}

// Write writes a raw IPv4 packet to the TUN (family prefix prepended internally).
func (d *Device) Write(buf []byte) (int, error) {
	bufs := [][]byte{buf}
	_, err := d.dev.Write(bufs, 0)
	if err != nil {
		return 0, err
	}
	return len(buf), nil
}

// Close destroys the TUN device and removes the interface.
func (d *Device) Close() error {
	return d.dev.Close()
}

// SetupIP assigns an IP address to the TUN interface. Platform-specific.
func (d *Device) SetupIP(ip string) error {
	return setupIP(d.name, ip)
}

// CleanupIP removes the IP address and route from the TUN interface. Platform-specific.
func (d *Device) CleanupIP(ip string) {
	cleanupIP(d.name, ip)
}
