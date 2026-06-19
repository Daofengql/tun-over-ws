package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/netip"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/Daofengql/tun-over-ws/internal/logger"
	"github.com/Daofengql/tun-over-ws/internal/tun"
)

const (
	defaultMaxActive = 2
	defaultMaxTotal  = 3
	writeChSize      = 512
	monitorInterval  = 1 * time.Second
	stateUpdateInterval = 200 * time.Millisecond
)

// pooledConn wraps a WebSocket connection with its state.
type pooledConn struct {
	ws    *websocket.Conn
	state *ConnState
	writeCh chan []byte
	vip   netip.Addr
	alive bool
}

// Pool manages multiple WebSocket connections with QoS-aware routing.
type Pool struct {
	mu   sync.RWMutex
	conns []*pooledConn

	serverURL string
	uuid      string
	token     string

	tunDev    *tun.Device
	virtualIP netip.Addr

	maxActive     int
	maxTotal      int
	timeoutDetect *TimeoutDetector
	rateLimiter   *RateLimiter

	log zerolog.Logger
}

// NewPool creates a connection pool.
func NewPool(serverURL, uuid, token string) *Pool {
	return &Pool{
		serverURL:     serverURL,
		uuid:          uuid,
		token:         token,
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
	dev, err := tun.Create("wsvpn0", 1280)
	if err != nil {
		pc.ws.CloseNow()
		return fmt.Errorf("create tun: %w", err)
	}
	p.tunDev = dev
	if err := dev.SetupIP(pc.vip.String()); err != nil {
		dev.Close()
		pc.ws.CloseNow()
		return fmt.Errorf("setup tun ip: %w", err)
	}

	// Start write and read loops for this connection.
	go p.writeConn(ctx, pc)
	go p.readConn(ctx, pc)

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
	buf := make([]byte, 1500)
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
		msgType, data, err := pc.ws.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log.Debug().Err(err).Msg("conn read ended")
			pc.alive = false
			return
		}

		if msgType == websocket.MessageText {
			continue
		}

		if p.tunDev != nil {
			p.tunDev.Write(data)
		}
	}
}

// writeConn drains a connection's writeCh and sends over WebSocket.
func (p *Pool) writeConn(ctx context.Context, pc *pooledConn) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-pc.writeCh:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := pc.ws.Write(writeCtx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				p.log.Debug().Err(err).Msg("conn write failed")
				pc.alive = false
				return
			}
			pc.state.RecordBytes(len(data))
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
		if !pc.alive {
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
		if pc.alive {
			alive++
			if pc.state.IsThrottled() {
				throttled++
			}
		}
	}

	// Remove dead connections.
	var live []*pooledConn
	for _, pc := range p.conns {
		if pc.alive {
			live = append(live, pc)
		} else {
			p.timeoutDetect.RecordDisconnect(pc.state.Age())
			pc.ws.CloseNow()
		}
	}
	p.conns = live

	// Check rotation: if oldest active connection is near timeout limit.
	rotationInterval := p.timeoutDetect.GetRotationInterval()
	for _, pc := range p.conns {
		if pc.state.Age() > rotationInterval && alive <= p.maxActive {
			p.log.Info().
				Dur("age", pc.state.Age()).
				Dur("rotation", rotationInterval).
				Msg("rotation threshold reached, building standby")
			go p.buildStandby(ctx)
			break
		}
	}

	// If all active are throttled and we have capacity, build more.
	if throttled == alive && alive > 0 && len(p.conns) < p.maxTotal {
		p.log.Info().Int("throttled", throttled).Msg("all throttled, building new conn")
		go p.buildStandby(ctx)
	}

	// Update rate limiter.
	var states []*ConnState
	for _, pc := range p.conns {
		if pc.alive {
			states = append(states, pc.state)
		}
	}
	if len(states) > 0 {
		p.rateLimiter.UpdateCapacity(states)
	}
}

// buildStandby creates a new connection and adds it to the pool.
func (p *Pool) buildStandby(ctx context.Context) {
	pc, err := p.buildConn(ctx)
	if err != nil {
		p.log.Error().Err(err).Msg("build standby failed")
		return
	}

	p.mu.Lock()
	if len(p.conns) >= p.maxTotal {
		p.mu.Unlock()
		pc.ws.CloseNow()
		return
	}
	p.conns = append(p.conns, pc)
	p.log.Info().Int("pool_size", len(p.conns)).Msg("standby added")
	p.mu.Unlock()

	// Start read and write loops for this connection.
	go p.writeConn(ctx, pc)
	go p.readConn(ctx, pc)
}

// buildConn performs WebSocket dial + hello handshake.
func (p *Pool) buildConn(ctx context.Context) (*pooledConn, error) {
	p.log.Info().Str("url", p.serverURL).Msg("building connection")

	wsConn, _, err := websocket.Dial(ctx, p.serverURL, nil)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}

	vip, err := p.hello(ctx, wsConn)
	if err != nil {
		wsConn.CloseNow()
		return nil, err
	}

	pc := &pooledConn{
		ws:      wsConn,
		state:   NewConnState(),
		writeCh: make(chan []byte, writeChSize),
		vip:     vip,
		alive:   true,
	}

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
		pc.alive = false
		pc.ws.CloseNow()
		close(pc.writeCh)
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
