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
	// allocated maps allocated IP -> UUID
	allocated map[netip.Addr]string
	// uuidToIP maps UUID -> allocated IP
	uuidToIP map[string]netip.Addr
}

// NewVIPAllocator creates a new allocator for the given CIDR.
// serverIP is reserved and will not be allocated.
func NewVIPAllocator(cidr string, serverIP netip.Addr) (*VIPAllocator, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}
	return &VIPAllocator{
		prefix:    prefix,
		serverIP:  serverIP,
		allocated: make(map[netip.Addr]string),
		uuidToIP: make(map[string]netip.Addr),
	}, nil
}

// Allocate returns an IP for the given UUID.
// If the UUID already has an allocation, it returns the same IP.
func (a *VIPAllocator) Allocate(uuid string) (netip.Addr, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if ip, ok := a.uuidToIP[uuid]; ok {
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
			a.allocated[addr] = uuid
			a.uuidToIP[uuid] = addr
			_ = mask // used for clarity
			return addr, nil
		}
	}
}

// Release removes the allocation for a UUID.
func (a *VIPAllocator) Release(uuid string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ip, ok := a.uuidToIP[uuid]; ok {
		delete(a.allocated, ip)
		delete(a.uuidToIP, uuid)
	}
}

// GetIP returns the allocated IP for a UUID, or zero Addr if not allocated.
func (a *VIPAllocator) GetIP(uuid string) (netip.Addr, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ip, ok := a.uuidToIP[uuid]
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
