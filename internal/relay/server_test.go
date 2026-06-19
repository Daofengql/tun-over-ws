package relay

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/daofeng/ws-vpn-go/internal/config"
)

func TestVIPAllocator(t *testing.T) {
	serverIP := netip.MustParseAddr("10.66.0.1")
	alloc, err := NewVIPAllocator("10.66.0.0/24", serverIP)
	if err != nil {
		t.Fatal(err)
	}

	ip1, err := alloc.Allocate("uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	if ip1.String() != "10.66.0.2" {
		t.Fatalf("expected 10.66.0.2, got %s", ip1)
	}

	ip2, err := alloc.Allocate("uuid-2")
	if err != nil {
		t.Fatal(err)
	}
	if ip2.String() != "10.66.0.3" {
		t.Fatalf("expected 10.66.0.3, got %s", ip2)
	}

	// Same UUID should get same IP.
	ip1Again, err := alloc.Allocate("uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	if ip1Again != ip1 {
		t.Fatalf("expected same IP %s, got %s", ip1, ip1Again)
	}

	// Server IP should be skipped.
	for i := 0; i < 250; i++ {
		uuid := "uuid-extra-" + string(rune('a'+i))
		ip, err := alloc.Allocate(uuid)
		if err != nil {
			t.Fatalf("failed at %d: %v", i, err)
		}
		if ip == serverIP {
			t.Fatalf("allocated server IP: %s", ip)
		}
	}
	t.Log("VIP allocator OK")
}

func buildTestPacket(src, dst netip.Addr) []byte {
	pkt := make([]byte, 28)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], 28)
	pkt[8] = 64
	pkt[9] = 255
	s4 := src.As4()
	d4 := dst.As4()
	copy(pkt[12:16], s4[:])
	copy(pkt[16:20], d4[:])
	return pkt
}

func TestRelayForwarding(t *testing.T) {
	// Start server.
	cfg := &config.ServerConfig{}
	cfg.Listen = "127.0.0.1:0" // random port
	cfg.OverlayCIDR = "10.66.0.0/24"
	cfg.ServerTUN = config.TUNConfig{
		IP:  "10.66.0.1",
		MTU: 1280,
	}
	cfg.Auth = config.AuthConfig{Tokens: []string{"test-token"}}
	cfg.Heartbeat = config.HeartbeatConf{Interval: 30 * time.Second}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server on random port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	cfg.Listen = addr
	srv, _ = NewServer(cfg)
	go srv.ListenAndServe(ctx)
	time.Sleep(200 * time.Millisecond)

	// Connect client A.
	wsURL := "ws://" + addr + "/tunnel"
	connA, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal("dial A:", err)
	}

	// Send hello from A.
	helloA := `{"type":"hello","uuid":"test-a","token":"test-token","hostname":"host-a"}`
	connA.Write(ctx, websocket.MessageText, []byte(helloA))

	_, respA, err := connA.Read(ctx)
	if err != nil {
		t.Fatal("read hello_ok A:", err)
	}
	t.Logf("A hello_ok: %s", respA)

	// Connect client B.
	connB, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal("dial B:", err)
	}

	helloB := `{"type":"hello","uuid":"test-b","token":"test-token","hostname":"host-b"}`
	connB.Write(ctx, websocket.MessageText, []byte(helloB))

	_, respB, err := connB.Read(ctx)
	if err != nil {
		t.Fatal("read hello_ok B:", err)
	}
	t.Logf("B hello_ok: %s", respB)

	// B sends a packet to A's VIP (10.66.0.2).
	srcB := netip.MustParseAddr("10.66.0.3")
	dstA := netip.MustParseAddr("10.66.0.2")
	pkt := buildTestPacket(srcB, dstA)

	t.Logf("B sending packet: %d bytes from %s to %s", len(pkt), srcB, dstA)
	err = connB.Write(ctx, websocket.MessageBinary, pkt)
	if err != nil {
		t.Fatal("B write:", err)
	}

	// A should receive the packet.
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()

	msgType, data, err := connA.Read(readCtx)
	if err != nil {
		t.Fatal("A read packet:", err)
	}
	if msgType != websocket.MessageBinary {
		t.Fatalf("expected binary, got %v", msgType)
	}
	if len(data) != 28 {
		t.Fatalf("expected 28 bytes, got %d", len(data))
	}

	parsed, err := parseIPv4Simple(data)
	if err != nil {
		t.Fatal("parse:", err)
	}
	t.Logf("A received packet: src=%s dst=%s len=%d", parsed.src, parsed.dst, len(data))

	if parsed.src != srcB {
		t.Fatalf("expected src %s, got %s", srcB, parsed.src)
	}
	if parsed.dst != dstA {
		t.Fatalf("expected dst %s, got %s", dstA, parsed.dst)
	}

	t.Log("RELAY FORWARDING OK")
}

type simplePkt struct {
	src netip.Addr
	dst netip.Addr
}

func parseIPv4Simple(data []byte) (*simplePkt, error) {
	src := netip.AddrFrom4([4]byte(data[12:16]))
	dst := netip.AddrFrom4([4]byte(data[16:20]))
	return &simplePkt{src: src, dst: dst}, nil
}
