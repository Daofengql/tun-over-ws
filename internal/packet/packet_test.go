package packet

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestParseIPv4(t *testing.T) {
	// Build a minimal valid IPv4 packet.
	pkt := make([]byte, 28)
	pkt[0] = 0x45 // version=4, IHL=5
	binary.BigEndian.PutUint16(pkt[2:4], 28)
	pkt[8] = 64 // TTL
	pkt[9] = 6  // TCP
	s4 := netip.MustParseAddr("10.66.0.2").As4()
	d4 := netip.MustParseAddr("10.66.0.3").As4()
	copy(pkt[12:16], s4[:])
	copy(pkt[16:20], d4[:])

	p, err := ParseIPv4(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 4 {
		t.Fatalf("version: %d", p.Version)
	}
	if p.SrcAddr.String() != "10.66.0.2" {
		t.Fatalf("src: %s", p.SrcAddr)
	}
	if p.DstAddr.String() != "10.66.0.3" {
		t.Fatalf("dst: %s", p.DstAddr)
	}
	if p.Protocol != 6 {
		t.Fatalf("protocol: %d", p.Protocol)
	}
}

func TestParseIPv4_TooShort(t *testing.T) {
	_, err := ParseIPv4([]byte{0x45, 0x00})
	if err == nil {
		t.Fatal("expected error for short packet")
	}
}

func TestParseIPv4_NotIPv4(t *testing.T) {
	pkt := make([]byte, 20)
	pkt[0] = 0x60 // version=6
	_, err := ParseIPv4(pkt)
	if err == nil {
		t.Fatal("expected error for IPv6")
	}
}

func TestParseIPv4_TotalLengthZero(t *testing.T) {
	pkt := make([]byte, 20)
	pkt[0] = 0x45
	_, err := ParseIPv4(pkt)
	if err == nil {
		t.Fatal("expected error for zero total length")
	}
}

func TestParseIPv4_TotalLengthShorterThanHeader(t *testing.T) {
	pkt := make([]byte, 20)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], 19)
	_, err := ParseIPv4(pkt)
	if err == nil {
		t.Fatal("expected error for total length shorter than header")
	}
}
