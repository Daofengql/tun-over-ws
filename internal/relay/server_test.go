package relay

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Daofengql/tun-over-ws/internal/config"
	"github.com/Daofengql/tun-over-ws/internal/packet"
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

func buildTestPacketWithProtocol(src, dst netip.Addr, protocol uint8) []byte {
	pkt := buildTestPacket(src, dst)
	pkt[9] = protocol
	return pkt
}

func buildTestTCPPacket(src, dst netip.Addr, srcPort, dstPort uint16, flags uint8) (*packet.Packet, []byte) {
	pkt := make([]byte, 40)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], 40)
	pkt[8] = 64
	pkt[9] = packet.ProtocolTCP
	s4 := src.As4()
	d4 := dst.As4()
	copy(pkt[12:16], s4[:])
	copy(pkt[16:20], d4[:])
	binary.BigEndian.PutUint16(pkt[20:22], srcPort)
	binary.BigEndian.PutUint16(pkt[22:24], dstPort)
	pkt[32] = 0x50
	pkt[33] = flags

	parsed, err := packet.ParseIPv4(pkt)
	if err != nil {
		panic(err)
	}
	return parsed, pkt
}

func newTestRelayClient(queueSize int) *client {
	return &client{
		WriteCh: make(chan []byte, queueSize),
		done:    make(chan struct{}),
	}
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

func TestServerEnqueueTCPWaitsForPrimaryQueue(t *testing.T) {
	srv := &Server{}
	primary := newTestRelayClient(1)
	primary.WriteCh <- []byte("queued")
	pkt, raw := buildTestTCPPacket(
		netip.MustParseAddr("10.66.0.2"),
		netip.MustParseAddr("10.66.0.3"),
		1000,
		443,
		packet.TCPFlagSYN,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan bool, 1)
	go func() {
		result <- srv.enqueueTCP(ctx, []*client{primary}, pkt, raw)
	}()

	select {
	case got := <-result:
		t.Fatalf("enqueueTCP returned before primary queue drained: %v", got)
	case <-time.After(50 * time.Millisecond):
	}

	<-primary.WriteCh

	select {
	case got := <-result:
		if !got {
			t.Fatal("enqueueTCP returned false after queue space became available")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueueTCP did not complete after queue space became available")
	}
}

func TestServerEnqueueUDPUsesStandbyWhenPrimaryFull(t *testing.T) {
	srv := &Server{}
	primary := newTestRelayClient(1)
	standby := newTestRelayClient(1)
	primary.WriteCh <- []byte("queued")

	if ok := srv.enqueueUDP(context.Background(), []*client{primary, standby}, []byte("udp")); !ok {
		t.Fatal("enqueueUDP returned false")
	}
	if len(primary.WriteCh) != 1 {
		t.Fatalf("primary queue changed: %d", len(primary.WriteCh))
	}
	if got := string(<-standby.WriteCh); got != "udp" {
		t.Fatalf("standby packet: got %q want udp", got)
	}
}

func TestServerEnqueueTCPRetriesStandbyWhenPrimaryCloses(t *testing.T) {
	srv := &Server{}
	primary := newTestRelayClient(1)
	standby := newTestRelayClient(1)
	primary.WriteCh <- []byte("queued")
	pkt, raw := buildTestTCPPacket(
		netip.MustParseAddr("10.66.0.2"),
		netip.MustParseAddr("10.66.0.3"),
		1001,
		443,
		packet.TCPFlagACK,
	)

	result := make(chan bool, 1)
	go func() {
		result <- srv.enqueueTCP(context.Background(), []*client{primary, standby}, pkt, raw)
	}()

	select {
	case got := <-result:
		t.Fatalf("enqueueTCP returned before primary closed: %v", got)
	case <-time.After(50 * time.Millisecond):
	}

	primary.markClosed()

	select {
	case got := <-result:
		if !got {
			t.Fatal("enqueueTCP returned false after standby became target")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueueTCP did not retry standby after primary closed")
	}

	if got := <-standby.WriteCh; string(got) != string(raw) {
		t.Fatalf("standby packet mismatch")
	}
}

func TestServerEnqueueTCPDoesNotDuplicateAfterQueueAccepts(t *testing.T) {
	srv := &Server{}
	primary := newTestRelayClient(1)
	standby := newTestRelayClient(1)
	pkt, raw := buildTestTCPPacket(
		netip.MustParseAddr("10.66.0.2"),
		netip.MustParseAddr("10.66.0.3"),
		1002,
		443,
		packet.TCPFlagSYN,
	)

	if ok := srv.enqueueTCP(context.Background(), []*client{primary, standby}, pkt, raw); !ok {
		t.Fatal("enqueueTCP returned false")
	}
	primary.markClosed()

	if got := <-primary.WriteCh; string(got) != string(raw) {
		t.Fatalf("primary packet mismatch")
	}
	if len(standby.WriteCh) != 0 {
		t.Fatalf("standby queue length: got %d want 0", len(standby.WriteCh))
	}
}

func TestServerEnqueueTCPNewFlowCanBurstToStandbyWhenPrimaryPressured(t *testing.T) {
	srv := &Server{tcpFlows: make(map[packet.TCPFlowKey]tcpFlowBinding)}
	primary := newTestRelayClient(1)
	standby := newTestRelayClient(1)
	primary.WriteCh <- []byte("queued")
	pkt, raw := buildTestTCPPacket(
		netip.MustParseAddr("10.66.0.2"),
		netip.MustParseAddr("10.66.0.3"),
		1003,
		443,
		packet.TCPFlagSYN,
	)

	if ok := srv.enqueueTCP(context.Background(), []*client{primary, standby}, pkt, raw); !ok {
		t.Fatal("enqueueTCP returned false")
	}
	if len(primary.WriteCh) != 1 {
		t.Fatalf("primary queue length: got %d want 1", len(primary.WriteCh))
	}
	if got := <-standby.WriteCh; string(got) != string(raw) {
		t.Fatalf("standby packet mismatch")
	}
}

func TestServerEnqueueTCPExistingFlowStaysBound(t *testing.T) {
	srv := &Server{tcpFlows: make(map[packet.TCPFlowKey]tcpFlowBinding)}
	primary := newTestRelayClient(2)
	standby := newTestRelayClient(2)
	primary.WriteCh <- []byte("queued")

	synPkt, synRaw := buildTestTCPPacket(
		netip.MustParseAddr("10.66.0.2"),
		netip.MustParseAddr("10.66.0.3"),
		1004,
		443,
		packet.TCPFlagSYN,
	)
	if ok := srv.enqueueTCP(context.Background(), []*client{primary, standby}, synPkt, synRaw); !ok {
		t.Fatal("initial SYN enqueue returned false")
	}
	<-standby.WriteCh

	ackPkt, ackRaw := buildTestTCPPacket(
		netip.MustParseAddr("10.66.0.2"),
		netip.MustParseAddr("10.66.0.3"),
		1004,
		443,
		packet.TCPFlagACK,
	)
	if ok := srv.enqueueTCP(context.Background(), []*client{primary, standby}, ackPkt, ackRaw); !ok {
		t.Fatal("existing flow enqueue returned false")
	}
	if len(primary.WriteCh) != 1 {
		t.Fatalf("primary queue length: got %d want 1", len(primary.WriteCh))
	}
	if got := <-standby.WriteCh; string(got) != string(ackRaw) {
		t.Fatalf("standby packet mismatch")
	}
}
