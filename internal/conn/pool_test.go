package conn

import (
	"context"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"github.com/Daofengql/tun-over-ws/internal/packet"
)

const testRotationInterval = 50 * time.Second

func newTestPooledConn(role connRole, queueSize int) *pooledConn {
	pc := &pooledConn{
		state:   NewConnState(),
		writeCh: make(chan []byte, queueSize),
		vip:     netip.MustParseAddr("10.66.0.2"),
		done:    make(chan struct{}),
	}
	pc.setRole(role)
	pc.alive.Store(true)
	return pc
}

func newTestTCPPacket(srcPort, dstPort uint16, flags uint8) (*packet.Packet, []byte) {
	raw := make([]byte, 40)
	raw[0] = 0x45
	binary.BigEndian.PutUint16(raw[2:4], uint16(len(raw)))
	raw[8] = 64
	raw[9] = packet.ProtocolTCP
	src := netip.MustParseAddr("10.66.0.2").As4()
	dst := netip.MustParseAddr("10.66.0.3").As4()
	copy(raw[12:16], src[:])
	copy(raw[16:20], dst[:])
	binary.BigEndian.PutUint16(raw[20:22], srcPort)
	binary.BigEndian.PutUint16(raw[22:24], dstPort)
	raw[32] = 0x50
	raw[33] = flags

	pkt, err := packet.ParseIPv4(raw)
	if err != nil {
		panic(err)
	}
	return pkt, raw
}

func TestPoolEnqueueTCPBlocksOnPrimary(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	primary := newTestPooledConn(rolePrimary, 1)
	primary.writeCh <- []byte("queued")
	p.conns = []*pooledConn{primary}
	pkt, raw := newTestTCPPacket(1000, 443, packet.TCPFlagSYN)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan bool, 1)
	go func() {
		result <- p.enqueueTCP(ctx, pkt, raw)
	}()

	select {
	case got := <-result:
		t.Fatalf("enqueueTCP returned before primary queue drained: %v", got)
	case <-time.After(50 * time.Millisecond):
	}

	<-primary.writeCh

	select {
	case got := <-result:
		if !got {
			t.Fatal("enqueueTCP returned false after queue space became available")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueueTCP did not complete after queue space became available")
	}

	if got := <-primary.writeCh; string(got) != string(raw) {
		t.Fatalf("queued packet mismatch")
	}
}

func TestPooledConnEnqueueSucceedsAfterQueueAccepts(t *testing.T) {
	pc := newTestPooledConn(rolePrimary, 1)

	ok, closed := pc.enqueue(context.Background(), []byte("tcp"))
	if !ok || closed {
		t.Fatalf("enqueue: ok=%v closed=%v, want ok=true closed=false", ok, closed)
	}

	pc.close()
	if got := string(<-pc.writeCh); got != "tcp" {
		t.Fatalf("queued packet: got %q want tcp", got)
	}
}

func TestPoolEnqueueUDPUsesStandbyWhenPrimaryFull(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.writeCh <- []byte("queued")
	p.conns = []*pooledConn{primary, standby}

	if ok := p.enqueueUDP(context.Background(), []byte("udp")); !ok {
		t.Fatal("enqueueUDP returned false")
	}
	if len(primary.writeCh) != 1 {
		t.Fatalf("primary queue changed: %d", len(primary.writeCh))
	}
	if got := string(<-standby.writeCh); got != "udp" {
		t.Fatalf("standby packet: got %q want udp", got)
	}
}

func TestPoolEnqueueTCPNewFlowCanBurstToStandbyWhenPrimaryDegraded(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.state.MarkDegraded("test")
	p.conns = []*pooledConn{primary, standby}
	pkt, raw := newTestTCPPacket(2000, 443, packet.TCPFlagSYN)

	if ok := p.enqueueTCP(context.Background(), pkt, raw); !ok {
		t.Fatal("enqueueTCP returned false")
	}
	if len(primary.writeCh) != 0 {
		t.Fatalf("primary queue length: got %d want 0", len(primary.writeCh))
	}
	if got := <-standby.writeCh; string(got) != string(raw) {
		t.Fatalf("standby packet mismatch")
	}
}

func TestPoolEnqueueTCPExistingFlowStaysBound(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	primary := newTestPooledConn(rolePrimary, 2)
	standby := newTestPooledConn(roleStandby, 2)
	primary.state.MarkDegraded("test")
	p.conns = []*pooledConn{primary, standby}

	synPkt, synRaw := newTestTCPPacket(2001, 443, packet.TCPFlagSYN)
	if ok := p.enqueueTCP(context.Background(), synPkt, synRaw); !ok {
		t.Fatal("initial SYN enqueue returned false")
	}
	<-standby.writeCh

	ackPkt, ackRaw := newTestTCPPacket(2001, 443, packet.TCPFlagACK)
	if ok := p.enqueueTCP(context.Background(), ackPkt, ackRaw); !ok {
		t.Fatal("existing flow enqueue returned false")
	}
	if len(primary.writeCh) != 0 {
		t.Fatalf("primary queue length: got %d want 0", len(primary.writeCh))
	}
	if got := <-standby.writeCh; string(got) != string(ackRaw) {
		t.Fatalf("standby packet mismatch")
	}
}

func TestPoolPromotesStandbyWhenPrimaryDead(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.close()
	p.conns = []*pooledConn{standby}

	p.ensurePrimaryLocked(context.Background())

	if standby.role != rolePrimary {
		t.Fatalf("standby role: got %s want %s", standby.role, rolePrimary)
	}
	if got := p.primaryLocked(); got != standby {
		t.Fatal("promoted standby is not primary")
	}
}

func TestPoolNormalizePrimaryKeepsOnePrimary(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	first := newTestPooledConn(rolePrimary, 1)
	second := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	p.conns = []*pooledConn{first, second, standby}

	got := p.normalizePrimaryLocked()
	if got != first {
		t.Fatal("normalizePrimaryLocked did not keep the first alive primary")
	}

	primaryCount := 0
	for _, pc := range p.conns {
		if pc.role == rolePrimary {
			primaryCount++
		}
	}
	if primaryCount != 1 {
		t.Fatalf("primary count: got %d want 1", primaryCount)
	}
	if second.role != roleStandby {
		t.Fatalf("second primary role: got %s want %s", second.role, roleStandby)
	}
}

func TestPoolEnsureStandbysRespectsMaxTotal(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	p.maxTotal = 3
	p.conns = []*pooledConn{
		newTestPooledConn(rolePrimary, 1),
	}

	p.ensureStandbysLocked(context.Background())

	if p.pendingBuilds != 2 {
		t.Fatalf("pendingBuilds: got %d want 2", p.pendingBuilds)
	}

	p.ensureStandbysLocked(context.Background())
	if p.pendingBuilds != 2 {
		t.Fatalf("pendingBuilds after second ensure: got %d want 2", p.pendingBuilds)
	}
}

func TestPoolRotatePrimaryPromotesStandby(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	p.maxTotal = 2
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	p.conns = []*pooledConn{primary, standby}

	p.rotatePrimaryLocked(context.Background(), "test")

	if standby.role != rolePrimary {
		t.Fatalf("standby role: got %s want %s", standby.role, rolePrimary)
	}
	if primary.role != roleDraining {
		t.Fatalf("old primary role: got %s want %s", primary.role, roleDraining)
	}
}

func TestPoolMonitorRotatesPrimaryAtTimeoutThreshold(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	p.maxTotal = 2
	p.timeoutDetect = NewTimeoutDetector(testRotationInterval)
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.primarySince = time.Now().Add(-testRotationInterval - time.Second)
	p.conns = []*pooledConn{primary, standby}

	p.monitorTick(context.Background())

	if standby.role != rolePrimary {
		t.Fatalf("standby role: got %s want %s", standby.role, rolePrimary)
	}
	if primary.role != roleDraining {
		t.Fatalf("old primary role: got %s want %s", primary.role, roleDraining)
	}
	if !primary.plannedClose.Load() {
		t.Fatal("old primary should be marked as planned close")
	}
}

func TestPoolMonitorDoesNotRotateNewPrimaryByConnectionAge(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	p.maxTotal = 2
	p.timeoutDetect = NewTimeoutDetector(testRotationInterval)
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.state.createdAt = time.Now().Add(-testRotationInterval - time.Second)
	primary.primarySince = time.Now()
	p.conns = []*pooledConn{primary, standby}

	p.monitorTick(context.Background())

	if primary.role != rolePrimary {
		t.Fatalf("primary role: got %s want %s", primary.role, rolePrimary)
	}
	if standby.role != roleStandby {
		t.Fatalf("standby role: got %s want %s", standby.role, roleStandby)
	}
	if p.pendingBuilds != 0 {
		t.Fatalf("pendingBuilds: got %d want 0", p.pendingBuilds)
	}
}

func TestPoolMonitorIgnoresPlannedCloseForTimeoutDetection(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	pc := newTestPooledConn(roleDraining, 1)
	pc.state.createdAt = time.Now().Add(-2 * testRotationInterval)
	pc.plannedClose.Store(true)
	pc.close()
	p.conns = []*pooledConn{pc}

	p.monitorTick(context.Background())

	if p.timeoutDetect.IsDetected() {
		t.Fatal("planned close should not feed timeout detector")
	}
}

func TestPoolMonitorDoesNotRotateWithoutDetectedTimeout(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	p.maxTotal = 2
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.primarySince = time.Now().Add(-2 * testRotationInterval)
	p.conns = []*pooledConn{primary, standby}

	p.monitorTick(context.Background())

	if primary.role != rolePrimary {
		t.Fatalf("primary role: got %s want %s", primary.role, rolePrimary)
	}
	if standby.role != roleStandby {
		t.Fatalf("standby role: got %s want %s", standby.role, roleStandby)
	}
}

func TestPoolMonitorBuildsStandbyOnSustainedWriteLatency(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	p.maxTotal = 3
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.state.RecordWrite(1, highWriteLatency)
	p.conns = []*pooledConn{primary, standby}

	for i := 0; i < latencyHighSamples-1; i++ {
		p.pendingBuilds = 0
		p.monitorTick(context.Background())
	}
	// Latency threshold not yet reached, no extra build from latency.
	p.pendingBuilds = 0
	p.monitorTick(context.Background())
	if p.pendingBuilds != 1 {
		t.Fatalf("pendingBuilds after sustained latency threshold: got %d want 1", p.pendingBuilds)
	}
	if !primary.state.IsDegraded() {
		t.Fatal("primary should be degraded after sustained high latency")
	}
}

func TestPoolMonitorRotatesPrimaryOnCriticalLatency(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	p.maxTotal = 3
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.state.RecordWrite(1, criticalWriteLatency)
	p.conns = []*pooledConn{primary, standby}

	for i := 0; i < latencyCriticalTicks; i++ {
		p.monitorTick(context.Background())
	}

	if primary.role != roleDraining {
		t.Fatalf("old primary role: got %s want %s", primary.role, roleDraining)
	}
	if standby.role != rolePrimary {
		t.Fatalf("standby role: got %s want %s", standby.role, rolePrimary)
	}
}
