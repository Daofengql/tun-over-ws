package conn

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

func newTestPooledConn(role connRole, queueSize int) *pooledConn {
	pc := &pooledConn{
		state:   NewConnState(),
		writeCh: make(chan []byte, queueSize),
		vip:     netip.MustParseAddr("10.66.0.2"),
		role:    role,
		done:    make(chan struct{}),
	}
	pc.alive.Store(true)
	return pc
}

func TestPoolEnqueueTCPBlocksOnPrimary(t *testing.T) {
	p := NewPool("ws://example.invalid/tunnel", "uuid", "token", "wsvpn-test", 1280)
	primary := newTestPooledConn(rolePrimary, 1)
	primary.writeCh <- []byte("queued")
	p.conns = []*pooledConn{primary}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan bool, 1)
	go func() {
		result <- p.enqueueTCP(ctx, []byte("tcp"))
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

	if got := string(<-primary.writeCh); got != "tcp" {
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
	primary := newTestPooledConn(rolePrimary, 1)
	standby := newTestPooledConn(roleStandby, 1)
	primary.state.createdAt = time.Now().Add(-defaultRotation - time.Second)
	p.conns = []*pooledConn{primary, standby}

	p.monitorTick(context.Background())

	if standby.role != rolePrimary {
		t.Fatalf("standby role: got %s want %s", standby.role, rolePrimary)
	}
	if primary.role != roleDraining {
		t.Fatalf("old primary role: got %s want %s", primary.role, roleDraining)
	}
}
