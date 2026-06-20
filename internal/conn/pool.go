package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/Daofengql/tun-over-ws/internal/logger"
	"github.com/Daofengql/tun-over-ws/internal/packet"
	"github.com/Daofengql/tun-over-ws/internal/tun"
)

const (
	defaultMaxTotal      = 3
	writeChSize          = 512
	monitorInterval      = 1 * time.Second
	stateUpdateInterval  = 200 * time.Millisecond
	connectionTimeout    = 15 * time.Second
	poolPingInterval     = 30 * time.Second
	poolReadTimeout      = 90 * time.Second
	poolWriteTimeout     = 30 * time.Second
	waitPrimaryInterval  = 100 * time.Millisecond
	udpBurstWait         = 10 * time.Millisecond
	icmpEnqueueWait      = 100 * time.Millisecond
	queueHighWatermark   = 0.8
	queuePressureSamples = 3
	highWriteLatency     = 2 * time.Second
	latencyHighSamples   = 3
	criticalWriteLatency = 5 * time.Second
	latencyCriticalTicks = 3
	flowIdleTimeout      = 2 * time.Minute
)

type connRole string

const (
	rolePrimary  connRole = "primary"
	roleStandby  connRole = "standby"
	roleDraining connRole = "draining"
)

// pooledConn wraps a WebSocket connection with its state.
type pooledConn struct {
	ws      *websocket.Conn
	state   *ConnState
	writeCh chan []byte
	vip     netip.Addr
	role    connRole

	writeMu   sync.Mutex
	alive     atomic.Bool
	closeOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

type tcpFlowBinding struct {
	conn   *pooledConn
	lastAt time.Time
}

// Pool manages multiple WebSocket connections with QoS-aware routing.
type Pool struct {
	mu         sync.RWMutex
	tunWriteMu sync.Mutex
	conns      []*pooledConn
	tcpFlows   map[packet.TCPFlowKey]tcpFlowBinding

	serverURL string
	uuid      string
	token     string
	tunName   string
	mtu       int

	tunDev    *tun.Device
	virtualIP netip.Addr

	maxTotal             int
	pendingBuilds        int
	timeoutDetect        *TimeoutDetector
	pressureTicks        int
	latencyTicks         int
	criticalLatencyTicks int

	log zerolog.Logger
}

// NewPool creates a connection pool.
func NewPool(serverURL, uuid, token, tunName string, mtu int) *Pool {
	if tunName == "" {
		tunName = "wsvpn0"
	}
	if mtu <= 0 {
		mtu = tun.DefaultMTU
	}
	return &Pool{
		serverURL:     serverURL,
		uuid:          uuid,
		token:         token,
		tunName:       tunName,
		mtu:           mtu,
		maxTotal:      defaultMaxTotal,
		timeoutDetect: NewTimeoutDetector(0),
		tcpFlows:      make(map[packet.TCPFlowKey]tcpFlowBinding),
		log:           logger.Logger.With().Str("component", "pool").Logger(),
	}
}

// Connect establishes the first connection, creates TUN, and configures IP.
func (p *Pool) Connect(ctx context.Context) error {
	pc, err := p.buildConn(ctx)
	if err != nil {
		return err
	}
	p.virtualIP = pc.vip

	// Create TUN.
	dev, err := tun.Create(p.tunName, p.mtu)
	if err != nil {
		pc.close()
		return fmt.Errorf("create tun: %w", err)
	}
	p.tunDev = dev
	if err := dev.SetupIP(pc.vip.String()); err != nil {
		dev.Close()
		pc.close()
		return fmt.Errorf("setup tun ip: %w", err)
	}

	p.startConn(ctx, pc)
	p.mu.Lock()
	pc.role = rolePrimary
	p.conns = append(p.conns, pc)
	p.mu.Unlock()
	p.ensureStandbys(ctx)

	p.log.Info().
		Str("vip", pc.vip.String()).
		Str("tun", dev.Name()).
		Int("pool_size", 1).
		Msg("pool connected")

	return nil
}

// VirtualIP returns the allocated virtual IP.
func (p *Pool) VirtualIP() netip.Addr {
	return p.virtualIP
}

// TunName returns the configured TUN interface name.
func (p *Pool) TunName() string {
	return p.tunName
}

// Run starts the pool: TUN pump, connection monitor, and state updater.
// Blocks until ctx is cancelled.
func (p *Pool) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// TUN → Pool (packet dispatch).
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		p.tunToPool(ctx)
	}()

	// Connection monitor (rotation, QoS, standby building).
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		p.monitor(ctx)
	}()

	// State updater (throughput tracking).
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		p.stateUpdater(ctx)
	}()

	<-ctx.Done()

	if p.tunDev != nil {
		p.tunDev.CleanupIP(p.virtualIP.String())
		p.tunDev.Close()
	}
	p.closeAll()

	wg.Wait()
	return nil
}

// tunToPool reads packets from TUN and dispatches by traffic class.
func (p *Pool) tunToPool(ctx context.Context) {
	bufSize := p.mtu
	if bufSize < 1500 {
		bufSize = 1500
	}
	buf := make([]byte, bufSize)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := p.tunDev.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if n == 0 {
			continue
		}

		raw := make([]byte, n)
		copy(raw, buf[:n])

		pkt, err := packet.ParseIPv4(raw)
		if err != nil {
			p.log.Debug().Err(err).Msg("tun: invalid packet")
			continue
		}
		p.log.Debug().
			Str("src", pkt.SrcAddr.String()).
			Str("dst", pkt.DstAddr.String()).
			Str("class", pkt.Class().String()).
			Int("bytes", n).
			Msg("tun -> pool")

		if !p.dispatchPacket(ctx, pkt, raw) && ctx.Err() == nil {
			p.log.Debug().
				Str("src", pkt.SrcAddr.String()).
				Str("dst", pkt.DstAddr.String()).
				Str("class", pkt.Class().String()).
				Int("bytes", n).
				Msg("packet dropped")
		}
	}
}

func (p *Pool) dispatchPacket(ctx context.Context, pkt *packet.Packet, raw []byte) bool {
	switch pkt.Class() {
	case packet.TrafficTCP:
		return p.enqueueTCP(ctx, pkt, raw)
	case packet.TrafficUDP:
		return p.enqueueUDP(ctx, raw)
	case packet.TrafficICMP, packet.TrafficOther:
		return p.enqueuePrimaryWithTimeout(ctx, raw, icmpEnqueueWait)
	case packet.TrafficNoise:
		return false
	default:
		return p.enqueuePrimaryWithTimeout(ctx, raw, icmpEnqueueWait)
	}
}

func (p *Pool) enqueueTCP(ctx context.Context, pkt *packet.Packet, raw []byte) bool {
	tcp, err := pkt.TCPHeader()
	if err != nil {
		p.log.Debug().Err(err).Msg("tcp: invalid header")
		return false
	}

	for ctx.Err() == nil {
		if pc := p.boundTCPConn(tcp.Flow); pc != nil {
			ok, closed := pc.enqueue(ctx, raw)
			if ok {
				p.afterTCPEnqueue(tcp, pc)
				return true
			}
			if closed {
				p.removeTCPFlow(tcp.Flow)
				continue
			}
			return false
		}

		pc := p.primaryOrWait(ctx)
		if pc == nil {
			return false
		}
		if tcp.IsInitialSYN() {
			if burst := p.tcpBurstStandby(); burst != nil {
				if ok := burst.tryEnqueue(raw); ok {
					p.afterTCPEnqueue(tcp, burst)
					return true
				}
			}
		}

		ok, closed := pc.enqueue(ctx, raw)
		if ok {
			p.afterTCPEnqueue(tcp, pc)
			return true
		}
		if closed {
			continue
		}
		return false
	}
	return false
}

func (p *Pool) enqueueUDP(ctx context.Context, raw []byte) bool {
	candidates := p.burstCandidates()
	for _, pc := range candidates {
		if ok := pc.tryEnqueue(raw); ok {
			return true
		}
	}

	primary := p.primary()
	if primary == nil || !primary.isAlive() {
		return false
	}

	return primary.enqueueWithTimeout(ctx, raw, udpBurstWait)
}

func (p *Pool) enqueuePrimaryWithTimeout(ctx context.Context, raw []byte, timeout time.Duration) bool {
	pc := p.primaryOrWait(ctx)
	if pc == nil {
		return false
	}

	return pc.enqueueWithTimeout(ctx, raw, timeout)
}

func (p *Pool) boundTCPConn(flow packet.TCPFlowKey) *pooledConn {
	p.mu.RLock()
	defer p.mu.RUnlock()

	binding, ok := p.tcpFlows[flow]
	if !ok || binding.conn == nil || !binding.conn.isAlive() || binding.conn.role == roleDraining {
		return nil
	}
	return binding.conn
}

func (p *Pool) afterTCPEnqueue(tcp *packet.TCPHeader, pc *pooledConn) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tcpFlows == nil {
		p.tcpFlows = make(map[packet.TCPFlowKey]tcpFlowBinding)
	}
	if tcp.ClosesFlow() {
		delete(p.tcpFlows, tcp.Flow)
		return
	}
	p.tcpFlows[tcp.Flow] = tcpFlowBinding{
		conn:   pc,
		lastAt: time.Now(),
	}
}

func (p *Pool) removeTCPFlow(flow packet.TCPFlowKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.tcpFlows, flow)
}

func (p *Pool) tcpBurstStandby() *pooledConn {
	p.mu.RLock()
	defer p.mu.RUnlock()

	primary := p.primaryLocked()
	if primary == nil || !p.primaryAllowsTCPBurstLocked(primary) {
		return nil
	}

	var best *pooledConn
	bestAvail := -1
	for _, pc := range p.conns {
		if pc.role != roleStandby || !pc.isAlive() {
			continue
		}
		avail := cap(pc.writeCh) - len(pc.writeCh)
		if avail > bestAvail {
			best = pc
			bestAvail = avail
		}
	}
	return best
}

func (p *Pool) primaryAllowsTCPBurstLocked(primary *pooledConn) bool {
	if primary == nil || primary.state.IsDegraded() {
		return primary != nil
	}
	if cap(primary.writeCh) == 0 {
		return false
	}
	return float64(len(primary.writeCh))/float64(cap(primary.writeCh)) >= queueHighWatermark
}

// readConn reads from a single connection and writes to TUN.
func (p *Pool) readConn(ctx context.Context, pc *pooledConn) {
	for {
		msgType, data, err := pc.read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log.Debug().Err(err).Msg("conn read ended")
			pc.close()
			return
		}

		if msgType == websocket.MessageText {
			continue
		}
		pc.state.RecordRead(len(data))

		pkt, err := packet.ParseIPv4(data)
		if err != nil {
			p.log.Debug().Err(err).Msg("invalid packet from server")
			continue
		}
		p.log.Debug().
			Str("src", pkt.SrcAddr.String()).
			Str("dst", pkt.DstAddr.String()).
			Int("bytes", len(data)).
			Msg("ws -> tun")

		if p.tunDev != nil {
			p.tunWriteMu.Lock()
			_, err = p.tunDev.Write(data)
			p.tunWriteMu.Unlock()
			if err != nil {
				p.log.Error().Err(err).Msg("tun write failed")
			}
		}
	}
}

// writeConn drains a connection's writeCh and sends over WebSocket.
func (p *Pool) writeConn(ctx context.Context, pc *pooledConn) {
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-pc.writeCh:
			if !ok {
				return
			}
			start := time.Now()
			err := pc.write(ctx, data)
			if err != nil {
				p.log.Debug().Err(err).Msg("conn write failed")
				pc.close()
				return
			}
			pc.state.RecordWrite(len(data), time.Since(start))
		}
	}
}

// heartbeatConn sends periodic WebSocket pings for a pooled connection.
func (p *Pool) heartbeatConn(ctx context.Context, pc *pooledConn) {
	ticker := time.NewTicker(poolPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := pc.ping(ctx)
			if err != nil {
				p.log.Warn().Err(err).Msg("connection heartbeat failed")
				pc.close()
				return
			}
		}
	}
}

func (p *Pool) primary() *pooledConn {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, pc := range p.conns {
		if pc.role == rolePrimary && pc.isAlive() {
			return pc
		}
	}
	return nil
}

func (p *Pool) primaryOrWait(ctx context.Context) *pooledConn {
	for ctx.Err() == nil {
		if pc := p.primary(); pc != nil {
			return pc
		}

		p.mu.Lock()
		p.ensurePrimaryLocked(ctx)
		p.mu.Unlock()

		if pc := p.primary(); pc != nil {
			return pc
		}

		timer := time.NewTimer(waitPrimaryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (p *Pool) burstCandidates() []*pooledConn {
	p.mu.RLock()
	defer p.mu.RUnlock()

	candidates := make([]*pooledConn, 0, len(p.conns))
	for _, pc := range p.conns {
		if pc.role == rolePrimary && pc.isAlive() {
			candidates = append(candidates, pc)
		}
	}
	for _, pc := range p.conns {
		if pc.role == roleStandby && pc.isAlive() {
			candidates = append(candidates, pc)
		}
	}
	return candidates
}

func (p *Pool) ensurePrimaryLocked(ctx context.Context) {
	if primary := p.normalizePrimaryLocked(); primary != nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	p.scheduleBuildLocked(ctx)
}

func (p *Pool) normalizePrimaryLocked() *pooledConn {
	var primary *pooledConn
	for _, pc := range p.conns {
		if !pc.isAlive() || pc.role != rolePrimary {
			continue
		}
		if primary == nil {
			primary = pc
		} else {
			pc.role = roleStandby
		}
	}
	if primary != nil {
		return primary
	}
	if promoted := p.promoteStandbyLocked(); promoted != nil {
		p.log.Info().Str("vip", promoted.vip.String()).Msg("standby promoted to primary")
		return promoted
	}
	return nil
}

func (p *Pool) promoteStandbyLocked() *pooledConn {
	for _, pc := range p.conns {
		if pc.role == roleStandby && pc.isAlive() {
			pc.role = rolePrimary
			return pc
		}
	}
	for _, pc := range p.conns {
		if pc.isAlive() && pc.role != roleDraining {
			pc.role = rolePrimary
			return pc
		}
	}
	return nil
}

func (p *Pool) rotatePrimaryLocked(ctx context.Context, reason string) {
	var current *pooledConn
	for _, pc := range p.conns {
		if pc.role == rolePrimary && pc.isAlive() {
			current = pc
			break
		}
	}
	if current == nil {
		p.ensurePrimaryLocked(ctx)
		return
	}

	var next *pooledConn
	for _, pc := range p.conns {
		if pc.role == roleStandby && pc.isAlive() {
			next = pc
			break
		}
	}
	if next == nil {
		p.scheduleBuildLocked(ctx)
		return
	}

	current.role = roleDraining
	next.role = rolePrimary
	p.log.Info().
		Str("reason", reason).
		Dur("old_age", current.state.Age()).
		Msg("primary rotated")
	go p.closeAfterDrain(current, 5*time.Second)
	p.scheduleBuildLocked(ctx)
}

func (p *Pool) closeAfterDrain(pc *pooledConn, maxWait time.Duration) {
	timer := time.NewTimer(maxWait)
	defer timer.Stop()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if len(pc.writeCh) == 0 {
			pc.close()
			return
		}
		select {
		case <-pc.done:
			return
		case <-ticker.C:
		case <-timer.C:
			pc.close()
			return
		}
	}
}

// monitor manages connection lifecycle: rotation, QoS, standby building.
func (p *Pool) monitor(ctx context.Context) {
	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mu.Lock()
			p.monitorTick(ctx)
			p.mu.Unlock()
		}
	}
}

func (p *Pool) monitorTick(ctx context.Context) {
	// Remove dead connections.
	var live []*pooledConn
	for _, pc := range p.conns {
		if pc.isAlive() {
			live = append(live, pc)
		} else {
			p.timeoutDetect.RecordDisconnect(pc.state.Age())
			pc.close()
		}
	}
	p.conns = live

	p.ensurePrimaryLocked(ctx)

	rotationInterval := p.timeoutDetect.GetRotationInterval()
	for _, pc := range p.conns {
		if pc.role == rolePrimary && pc.isAlive() && pc.state.Age() > rotationInterval {
			p.log.Info().
				Dur("age", pc.state.Age()).
				Dur("rotation", rotationInterval).
				Msg("rotation threshold reached")
			p.rotatePrimaryLocked(ctx, "rotation")
			break
		}
	}

	if primary := p.primaryLocked(); primary != nil && p.queuePressure(primary) {
		p.pressureTicks++
	} else {
		p.pressureTicks = 0
	}
	if p.pressureTicks >= queuePressureSamples {
		if primary := p.primaryLocked(); primary != nil {
			p.log.Debug().Int("queue", len(primary.writeCh)).Msg("primary queue under pressure")
			p.scheduleBuildLocked(ctx)
		}
	}

	if primary := p.primaryLocked(); primary != nil && primary.state.WriteLatencyEWMA() >= highWriteLatency {
		p.latencyTicks++
	} else {
		p.latencyTicks = 0
	}
	if p.latencyTicks >= latencyHighSamples {
		if primary := p.primaryLocked(); primary != nil {
			primary.state.MarkDegraded("write_latency")
			p.log.Debug().
				Dur("write_latency_ewma", primary.state.WriteLatencyEWMA()).
				Msg("primary write latency high")
			p.scheduleBuildLocked(ctx)
		}
	}

	// Critical sustained latency: actually rotate primary.
	if primary := p.primaryLocked(); primary != nil && primary.state.WriteLatencyEWMA() >= criticalWriteLatency {
		p.criticalLatencyTicks++
	} else {
		p.criticalLatencyTicks = 0
	}
	if p.criticalLatencyTicks >= latencyCriticalTicks {
		if primary := p.primaryLocked(); primary != nil {
			p.log.Warn().
				Dur("write_latency_ewma", primary.state.WriteLatencyEWMA()).
				Msg("critical sustained latency, rotating primary")
			p.rotatePrimaryLocked(ctx, "critical_latency")
		}
	}

	p.ensureStandbysLocked(ctx)
}

func (p *Pool) primaryLocked() *pooledConn {
	for _, pc := range p.conns {
		if pc.role == rolePrimary && pc.isAlive() {
			return pc
		}
	}
	return nil
}

func (p *Pool) queuePressure(pc *pooledConn) bool {
	if pc == nil || cap(pc.writeCh) == 0 {
		return false
	}
	depth := len(pc.writeCh)
	capacity := cap(pc.writeCh)
	pc.state.RecordQueue(depth, capacity)
	return float64(depth)/float64(capacity) >= queueHighWatermark
}

func (p *Pool) scheduleBuildLocked(ctx context.Context) {
	if len(p.conns)+p.pendingBuilds >= p.maxTotal {
		return
	}
	p.pendingBuilds++
	go p.buildStandby(ctx)
}

func (p *Pool) ensureStandbys(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureStandbysLocked(ctx)
}

func (p *Pool) ensureStandbysLocked(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	for len(p.conns)+p.pendingBuilds < p.maxTotal {
		p.scheduleBuildLocked(ctx)
	}
}

// buildStandby creates a new connection and adds it to the pool.
func (p *Pool) buildStandby(ctx context.Context) {
	defer func() {
		p.mu.Lock()
		p.pendingBuilds--
		p.mu.Unlock()
	}()

	pc, err := p.buildConn(ctx)
	if err != nil {
		p.log.Error().Err(err).Msg("build standby failed")
		return
	}

	p.mu.Lock()
	if len(p.conns) >= p.maxTotal {
		p.mu.Unlock()
		pc.close()
		return
	}
	if p.virtualIP.IsValid() && pc.vip != p.virtualIP {
		p.mu.Unlock()
		p.log.Error().
			Str("expected", p.virtualIP.String()).
			Str("got", pc.vip.String()).
			Msg("standby VIP mismatch")
		pc.close()
		return
	}
	pc.role = roleStandby
	p.conns = append(p.conns, pc)
	p.log.Info().Int("pool_size", len(p.conns)).Str("role", string(pc.role)).Msg("standby added")
	p.mu.Unlock()

	p.startConn(ctx, pc)
}

// buildConn performs WebSocket dial + hello handshake.
func (p *Pool) buildConn(ctx context.Context) (*pooledConn, error) {
	p.log.Info().Str("url", p.serverURL).Msg("building connection")

	connCtx, cancel := context.WithTimeout(ctx, connectionTimeout)
	defer cancel()

	wsConn, _, err := websocket.Dial(connCtx, p.serverURL, nil)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}

	vip, err := p.hello(connCtx, wsConn)
	if err != nil {
		wsConn.CloseNow()
		return nil, err
	}

	pc := &pooledConn{
		ws:      wsConn,
		state:   NewConnState(),
		writeCh: make(chan []byte, writeChSize),
		vip:     vip,
		role:    roleStandby,
		done:    make(chan struct{}),
	}
	pc.alive.Store(true)

	p.log.Info().Str("vip", vip.String()).Msg("connection built")
	return pc, nil
}

func (p *Pool) hello(ctx context.Context, wsConn *websocket.Conn) (netip.Addr, error) {
	hello := HelloMessage{
		Type:     "hello",
		UUID:     p.uuid,
		Token:    p.token,
		Hostname: hostname(),
		WantExit: false,
	}

	data, err := json.Marshal(hello)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("marshal hello: %w", err)
	}

	if err := wsConn.Write(ctx, websocket.MessageText, data); err != nil {
		return netip.Addr{}, fmt.Errorf("send hello: %w", err)
	}

	_, resp, err := wsConn.Read(ctx)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("read hello_ok: %w", err)
	}

	var ok HelloOK
	if err := json.Unmarshal(resp, &ok); err != nil {
		return netip.Addr{}, fmt.Errorf("parse hello_ok: %w", err)
	}
	if ok.Type != "hello_ok" {
		return netip.Addr{}, fmt.Errorf("unexpected response type: %s", ok.Type)
	}

	vip, err := netip.ParseAddr(ok.VirtualIP)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid virtual_ip: %w", err)
	}

	return vip, nil
}

func (p *Pool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pc := range p.conns {
		pc.close()
	}
	p.conns = nil
	p.tcpFlows = make(map[packet.TCPFlowKey]tcpFlowBinding)
}

// stateUpdater periodically updates all connection states.
func (p *Pool) stateUpdater(ctx context.Context) {
	ticker := time.NewTicker(stateUpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mu.RLock()
			for _, pc := range p.conns {
				pc.state.RecordQueue(len(pc.writeCh), cap(pc.writeCh))
				pc.state.Update()
			}
			p.mu.RUnlock()
			p.expireTCPFlows()
		}
	}
}

func (p *Pool) expireTCPFlows() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.tcpFlows) == 0 {
		return
	}
	now := time.Now()
	for flow, binding := range p.tcpFlows {
		if binding.conn == nil || !binding.conn.isAlive() || binding.conn.role == roleDraining || now.Sub(binding.lastAt) > flowIdleTimeout {
			delete(p.tcpFlows, flow)
		}
	}
}

func (p *Pool) startConn(parent context.Context, pc *pooledConn) {
	connCtx, cancel := context.WithCancel(parent)
	pc.cancel = cancel
	go p.writeConn(connCtx, pc)
	go p.readConn(connCtx, pc)
	go p.heartbeatConn(connCtx, pc)
}

func (pc *pooledConn) isAlive() bool {
	return pc.alive.Load()
}

func (pc *pooledConn) tryEnqueue(data []byte) bool {
	if !pc.isAlive() {
		return false
	}
	select {
	case <-pc.done:
		return false
	default:
	}
	select {
	case pc.writeCh <- data:
		return true
	case <-pc.done:
		return false
	default:
		return false
	}
}

func (pc *pooledConn) enqueue(ctx context.Context, data []byte) (ok bool, closed bool) {
	if !pc.isAlive() {
		return false, true
	}
	select {
	case <-pc.done:
		return false, true
	default:
	}
	select {
	case pc.writeCh <- data:
		return true, false
	case <-pc.done:
		return false, true
	case <-ctx.Done():
		return false, false
	}
}

func (pc *pooledConn) enqueueWithTimeout(ctx context.Context, data []byte, timeout time.Duration) bool {
	if timeout <= 0 {
		ok, _ := pc.enqueue(ctx, data)
		return ok
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	if !pc.isAlive() {
		return false
	}
	select {
	case <-pc.done:
		return false
	default:
	}
	select {
	case pc.writeCh <- data:
		return true
	case <-pc.done:
		return false
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (pc *pooledConn) close() {
	pc.closeOnce.Do(func() {
		pc.alive.Store(false)
		if pc.cancel != nil {
			pc.cancel()
		}
		if pc.ws != nil {
			pc.ws.CloseNow()
		}
		close(pc.done)
	})
}

func (pc *pooledConn) read(ctx context.Context) (websocket.MessageType, []byte, error) {
	readCtx, cancel := context.WithTimeout(ctx, poolReadTimeout)
	defer cancel()
	return pc.ws.Read(readCtx)
}

func (pc *pooledConn) write(ctx context.Context, data []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, poolWriteTimeout)
	defer cancel()
	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()
	return pc.ws.Write(writeCtx, websocket.MessageBinary, data)
}

func (pc *pooledConn) ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()
	return pc.ws.Ping(pingCtx)
}
