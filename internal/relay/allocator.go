package relay

import (
	"fmt"
	"net/netip"
	"sync"
)

// VIPAllocator dynamically allocates virtual IPs from a CIDR range.
type VIPAllocator struct {
	mu       sync.Mutex
	prefix   netip.Prefix
	serverIP netip.Addr
	// allocated maps allocated IP -> device ID
	allocated map[netip.Addr]string
	// deviceToIP maps device ID -> allocated IP
	deviceToIP map[string]netip.Addr
}

// NewVIPAllocator creates a new allocator for the given CIDR.
// serverIP is reserved and will not be allocated.
func NewVIPAllocator(cidr string, serverIP netip.Addr) (*VIPAllocator, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}
	return &VIPAllocator{
		prefix:     prefix,
		serverIP:   serverIP,
		allocated:  make(map[netip.Addr]string),
		deviceToIP: make(map[string]netip.Addr),
	}, nil
}

// Allocate returns an IP for the given device ID.
// If the device already has an allocation, it returns the same IP.
func (a *VIPAllocator) Allocate(deviceID string) (netip.Addr, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if ip, ok := a.deviceToIP[deviceID]; ok {
		return ip, nil
	}

	// Scan for next available IP in the prefix.
	// Skip: network address (x.x.x.0), server IP, broadcast (x.x.x.255).
	addr := a.prefix.Addr()
	mask := a.prefix.Bits()
	broadcast := broadcastAddr(a.prefix)

	for {
		addr = nextAddr(addr)
		if !a.prefix.Contains(addr) {
			return netip.Addr{}, fmt.Errorf("no available IP in %s", a.prefix)
		}
		// Skip server IP
		if addr == a.serverIP {
			continue
		}
		// Skip broadcast
		if addr == broadcast {
			continue
		}
		// Skip network address (first in /24)
		if networkAddr(a.prefix) == addr {
			continue
		}
		if _, taken := a.allocated[addr]; !taken {
			a.allocated[addr] = deviceID
			a.deviceToIP[deviceID] = addr
			_ = mask // used for clarity
			return addr, nil
		}
	}
}

// Release removes the allocation for a device ID.
func (a *VIPAllocator) Release(deviceID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ip, ok := a.deviceToIP[deviceID]; ok {
		delete(a.allocated, ip)
		delete(a.deviceToIP, deviceID)
	}
}

// GetIP returns the allocated IP for a device ID, or zero Addr if not allocated.
func (a *VIPAllocator) GetIP(deviceID string) (netip.Addr, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ip, ok := a.deviceToIP[deviceID]
	return ip, ok
}

func nextAddr(addr netip.Addr) netip.Addr {
	b := addr.As4()
	for i := 3; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			break
		}
	}
	return netip.AddrFrom4(b)
}

func networkAddr(prefix netip.Prefix) netip.Addr {
	return prefix.Masked().Addr()
}

func broadcastAddr(prefix netip.Prefix) netip.Addr {
	addr := prefix.Addr().As4()
	bits := prefix.Bits()
	for i := bits; i < 32; i++ {
		byteIdx := i / 8
		bitIdx := 7 - (i % 8)
		addr[byteIdx] |= 1 << bitIdx
	}
	return netip.AddrFrom4(addr)
}
