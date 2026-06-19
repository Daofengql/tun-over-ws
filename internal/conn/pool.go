package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
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
	defaultMaxActive    = 2
	defaultMaxTotal     = 3
	writeChSize         = 512
	monitorInterval     = 1 * time.Second
	stateUpdateInterval = 200 * time.Millisecond
	connectionTimeout   = 15 * time.Second
	poolPingInterval    = 30 * time.Second
	poolReadTimeout     = 90 * time.Second
	poolWriteTimeout    = 5 * time.Second
)

// pooledConn wraps a WebSocket connection with its state.
type pooledConn struct {
	ws      *websocket.Conn
	state   *ConnState
	writeCh chan []byte
	vip     netip.Addr

	writeMu   sync.Mutex
	alive     atomic.Bool
	closeOnce sync.Once
	cancel    context.CancelFunc
}

// Pool manages multiple WebSocket connections with QoS-aware routing.
type Pool struct {
	mu         sync.RWMutex
	tunWriteMu sync.Mutex
	conns      []*pooledConn

	serverURL string
	uuid      string
	token     string
	tunName   string
	mtu       int

	tunDev    *tun.Device
	virtualIP netip.Addr

	maxActive     int
	maxTotal      int
	pendingBuilds int
	timeoutDetect *TimeoutDetector
	rateLimiter   *RateLimiter

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
		maxActive:     defaultMaxActive,
		maxTotal:      defaultMaxTotal,
		timeoutDetect: NewTimeoutDetector(0),
		rateLimiter:   NewRateLimiter(10 * 1024 * 1024), // 10MB/s initial
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
	p.conns = append(p.conns, pc)
	p.mu.Unlock()

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

// tunToPool reads packets from TUN and dispatches to the best connection.
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

		// Rate limit check.
		if !p.rateLimiter.Allow(n) {
			p.log.Debug().Int("bytes", n).Msg("rate limited, dropping")
			continue
		}

		raw := make([]byte, n)
		copy(raw, buf[:n])

		pc := p.selectConn()
		if pc == nil {
			p.log.Warn().Msg("no available connection, dropping")
			continue
		}
		if !pc.isAlive() {
			p.log.Debug().Msg("selected connection died before dispatch")
			continue
		}

		select {
		case pc.writeCh <- raw:
		default:
			p.log.Warn().Msg("selected conn full, dropping")
		}
	}
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
			err := pc.write(ctx, data)
			if err != nil {
				p.log.Debug().Err(err).Msg("conn write failed")
				pc.close()
				return
			}
			pc.state.RecordBytes(len(data))
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

// selectConn picks the best connection using weighted random selection.
func (p *Pool) selectConn() *pooledConn {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Collect alive connections with positive weights.
	type candidate struct {
		conn   *pooledConn
		weight float64
	}
	var candidates []candidate
	var totalWeight float64

	for _, pc := range p.conns {
		if !pc.isAlive() {
			continue
		}
		w := pc.state.Weight()
		if w <= 0 {
			continue
		}
		candidates = append(candidates, candidate{pc, w})
		totalWeight += w
	}

	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return candidates[0].conn
	}

	// Weighted random.
	r := rand.Float64() * totalWeight
	var cum float64
	for _, c := range candidates {
		cum += c.weight
		if r <= cum {
			return c.conn
		}
	}
	return candidates[len(candidates)-1].conn
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
	// Count alive connections.
	alive := 0
	throttled := 0
	for _, pc := range p.conns {
		if pc.isAlive() {
			alive++
			if pc.state.IsThrottled() {
				throttled++
			}
		}
	}

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
	alive = 0
	throttled = 0
	for _, pc := range live {
		if pc.isAlive() {
			alive++
			if pc.state.IsThrottled() {
				throttled++
			}
		}
	}

	if alive == 0 {
		p.log.Warn().Msg("no alive connections, rebuilding")
		p.scheduleBuildLocked(ctx)
		return
	}

	// Check rotation: if oldest active connection is near timeout limit.
	rotationInterval := p.timeoutDetect.GetRotationInterval()
	for _, pc := range p.conns {
		if pc.state.Age() > rotationInterval && alive <= p.maxActive {
			p.log.Info().
				Dur("age", pc.state.Age()).
				Dur("rotation", rotationInterval).
				Msg("rotation threshold reached, building standby")
			p.scheduleBuildLocked(ctx)
			break
		}
	}

	// If all active are throttled and we have capacity, build more.
	if throttled == alive && alive > 0 && len(p.conns) < p.maxTotal {
		p.log.Info().Int("throttled", throttled).Msg("all throttled, building new conn")
		p.scheduleBuildLocked(ctx)
	}

	// Update rate limiter.
	var states []*ConnState
	for _, pc := range p.conns {
		if pc.isAlive() {
			states = append(states, pc.state)
		}
	}
	if len(states) > 0 {
		p.rateLimiter.UpdateCapacity(states)
	}
}

func (p *Pool) scheduleBuildLocked(ctx context.Context) {
	if len(p.conns)+p.pendingBuilds >= p.maxTotal {
		return
	}
	p.pendingBuilds++
	go p.buildStandby(ctx)
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
	p.conns = append(p.conns, pc)
	p.log.Info().Int("pool_size", len(p.conns)).Msg("standby added")
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
				pc.state.Update()
			}
			p.mu.RUnlock()
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

func (pc *pooledConn) close() {
	pc.closeOnce.Do(func() {
		pc.alive.Store(false)
		if pc.cancel != nil {
			pc.cancel()
		}
		pc.ws.CloseNow()
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
