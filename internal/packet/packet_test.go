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

func TestPacketClass(t *testing.T) {
	tests := []struct {
		name     string
		protocol uint8
		dst      string
		want     TrafficClass
	}{
		{name: "tcp", protocol: ProtocolTCP, dst: "10.66.0.3", want: TrafficTCP},
		{name: "udp", protocol: ProtocolUDP, dst: "10.66.0.3", want: TrafficUDP},
		{name: "icmp", protocol: ProtocolICMP, dst: "10.66.0.3", want: TrafficICMP},
		{name: "multicast", protocol: ProtocolUDP, dst: "224.0.0.251", want: TrafficNoise},
		{name: "broadcast", protocol: ProtocolUDP, dst: "255.255.255.255", want: TrafficNoise},
		{name: "igmp", protocol: ProtocolIGMP, dst: "10.66.0.3", want: TrafficNoise},
		{name: "other", protocol: 47, dst: "10.66.0.3", want: TrafficOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pkt := make([]byte, 28)
			pkt[0] = 0x45
			binary.BigEndian.PutUint16(pkt[2:4], 28)
			pkt[8] = 64
			pkt[9] = tt.protocol
			src := netip.MustParseAddr("10.66.0.2").As4()
			dst := netip.MustParseAddr(tt.dst).As4()
			copy(pkt[12:16], src[:])
			copy(pkt[16:20], dst[:])

			parsed, err := ParseIPv4(pkt)
			if err != nil {
				t.Fatal(err)
			}
			if got := parsed.Class(); got != tt.want {
				t.Fatalf("class: got %s want %s", got, tt.want)
			}
		})
	}
}

func TestTCPHeader(t *testing.T) {
	pkt := make([]byte, 40)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], 40)
	pkt[8] = 64
	pkt[9] = ProtocolTCP
	src := netip.MustParseAddr("10.66.0.2").As4()
	dst := netip.MustParseAddr("10.66.0.3").As4()
	copy(pkt[12:16], src[:])
	copy(pkt[16:20], dst[:])
	binary.BigEndian.PutUint16(pkt[20:22], 12345)
	binary.BigEndian.PutUint16(pkt[22:24], 443)
	pkt[32] = 0x50
	pkt[33] = TCPFlagSYN

	parsed, err := ParseIPv4(pkt)
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := parsed.TCPHeader()
	if err != nil {
		t.Fatal(err)
	}

	if tcp.Flow.SrcAddr != netip.MustParseAddr("10.66.0.2") {
		t.Fatalf("src addr: got %s", tcp.Flow.SrcAddr)
	}
	if tcp.Flow.DstAddr != netip.MustParseAddr("10.66.0.3") {
		t.Fatalf("dst addr: got %s", tcp.Flow.DstAddr)
	}
	if tcp.Flow.SrcPort != 12345 {
		t.Fatalf("src port: got %d want 12345", tcp.Flow.SrcPort)
	}
	if tcp.Flow.DstPort != 443 {
		t.Fatalf("dst port: got %d want 443", tcp.Flow.DstPort)
	}
	if !tcp.IsInitialSYN() {
		t.Fatal("expected initial SYN")
	}
}
