package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/Daofengql/tun-over-ws/internal/config"
	"github.com/Daofengql/tun-over-ws/internal/logger"
	"github.com/Daofengql/tun-over-ws/internal/packet"
	"github.com/Daofengql/tun-over-ws/internal/store"
)

const (
	pingInterval         = 30 * time.Second
	relayTCPEnqueueWait  = 30 * time.Second
	relayUDPEnqueueWait  = 10 * time.Millisecond
	relayICMPEnqueueWait = 100 * time.Millisecond
	relayQueueHighWater  = 0.8
	relayFlowIdleTimeout = 2 * time.Minute
)

// client represents a connected client.
type client struct {
	DeviceID   string
	VirtualIP  netip.Addr
	Conn       *websocket.Conn
	WriteCh    chan []byte
	RemoteAddr string
	writeMu    sync.Mutex
	closed     atomic.Bool
	closeOnce  sync.Once
	done       chan struct{}
}

type tcpFlowBinding struct {
	target *client
	lastAt time.Time
}

// Server is the WebSocket relay server.
type Server struct {
	cfg       *config.ServerConfig
	store     store.Store
	allocator *VIPAllocator

	mu       sync.RWMutex
	clients  map[string][]*client     // device_id -> connections
	vipMap   map[netip.Addr][]*client // VIP -> connections
	tcpFlows map[packet.TCPFlowKey]tcpFlowBinding

	overlayCIDR *net.IPNet // pre-parsed for fast matching

	log zerolog.Logger
}

// NewServer creates a new relay server.
func NewServer(cfg *config.ServerConfig, store store.Store) (*Server, error) {
	serverIP, err := netip.ParseAddr(cfg.ServerTUN.IP)
	if err != nil {
		return nil, fmt.Errorf("invalid server tun ip: %w", err)
	}

	alloc, err := NewVIPAllocator(cfg.OverlayCIDR, serverIP)
	if err != nil {
		return nil, fmt.Errorf("create vip allocator: %w", err)
	}

	_, cidr, err := net.ParseCIDR(cfg.OverlayCIDR)
	if err != nil {
		return nil, fmt.Errorf("parse overlay cidr: %w", err)
	}

	return &Server{
		cfg:         cfg,
		store:       store,
		allocator:   alloc,
		clients:     make(map[string][]*client),
		vipMap:      make(map[netip.Addr][]*client),
		tcpFlows:    make(map[packet.TCPFlowKey]tcpFlowBinding),
		overlayCIDR: cidr,
		log:         logger.Logger.With().Str("component", "relay").Logger(),
	}, nil
}

// ListenAndServe starts the HTTP server and listens for WebSocket connections.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		s.HandleTunnel(ctx, w, r)
	})

	server := &http.Server{
		Addr:    s.cfg.Listen,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	s.log.Info().Str("listen", s.cfg.Listen).Msg("relay server starting")
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// HandleTunnel handles incoming WebSocket tunnel connections.
func (s *Server) HandleTunnel(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	remoteAddr := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		remoteAddr = xff
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.log.Error().Err(err).Str("remote", remoteAddr).Msg("websocket accept failed")
		return
	}

	go s.handleConn(ctx, conn, remoteAddr)
}

func (s *Server) handleConn(ctx context.Context, conn *websocket.Conn, remoteAddr string) {
	// Step 1: Read hello message and validate device.
	helloCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	device, err := s.readHelloAndValidate(helloCtx, conn)
	cancel()
	if err != nil {
		s.log.Warn().Err(err).Str("remote", remoteAddr).Msg("hello failed")
		conn.Close(websocket.StatusPolicyViolation, "hello failed")
		return
	}

	// Step 2: Determine VIP from device config.
	vipStr := device.EffectiveIP()
	vip, err := netip.ParseAddr(vipStr)
	if err != nil {
		s.log.Error().Err(err).Str("device_id", device.DeviceID).Str("vip", vipStr).Msg("invalid device vip")
		conn.Close(websocket.StatusInternalError, "invalid vip configuration")
		return
	}

	// Step 3: Send hello_ok.
	if err := s.sendHelloOK(ctx, conn, vip); err != nil {
		s.log.Error().Err(err).Str("device_id", device.DeviceID).Msg("send hello_ok failed")
		conn.Close(websocket.StatusInternalError, "hello_ok failed")
		return
	}

	// Step 4: Register client. Multiple connections may share one device ID/VIP.
	c := &client{
		DeviceID:   device.DeviceID,
		VirtualIP:  vip,
		Conn:       conn,
		WriteCh:    make(chan []byte, 512),
		RemoteAddr: remoteAddr,
		done:       make(chan struct{}),
	}
	s.registerClient(c)
	defer s.unregisterClient(c)

	s.log.Info().
		Str("device_id", device.DeviceID).
		Str("vip", vip.String()).
		Str("remote", remoteAddr).
		Msg("client connected")

	// Step 5: Run read, write, and heartbeat loops.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	done := make(chan struct{})

	go func() {
		defer connCancel()
		defer close(done)
		s.writeLoop(connCtx, c)
	}()

	go func() {
		defer connCancel()
		s.serverHeartbeat(connCtx, c)
	}()

	s.readLoop(connCtx, c)
	connCancel()
	<-done
}

func (s *Server) readHelloAndValidate(ctx context.Context, conn *websocket.Conn) (*store.Device, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var hello struct {
		Type      string `json:"type"`
		DeviceID  string `json:"device_id"`
		AccessKey string `json:"ak"`
		Hostname  string `json:"hostname"`
	}
	if err := json.Unmarshal(data, &hello); err != nil {
		return nil, fmt.Errorf("parse hello: %w", err)
	}
	if hello.Type != "hello" {
		return nil, fmt.Errorf("expected hello, got %q", hello.Type)
	}
	if hello.DeviceID == "" {
		return nil, fmt.Errorf("empty device_id")
	}
	if hello.AccessKey == "" {
		return nil, fmt.Errorf("missing access key")
	}

	// Validate device by AK
	device, err := s.store.GetDeviceByAK(ctx, hashToken(hello.AccessKey))
	if err != nil {
		return nil, fmt.Errorf("lookup device: %w", err)
	}
	if device == nil {
		return nil, fmt.Errorf("invalid access key")
	}
	if device.DeviceID != hello.DeviceID {
		return nil, fmt.Errorf("device_id mismatch")
	}
	if device.Status != store.DeviceStatusApproved {
		return nil, fmt.Errorf("device not approved")
	}
	if device.KeyExpiresAt == nil || time.Now().After(*device.KeyExpiresAt) {
		return nil, fmt.Errorf("access key expired")
	}

	// Update last seen
	if err := s.store.UpdateDeviceLastSeen(ctx, device.DeviceID); err != nil {
		s.log.Warn().Err(err).Str("device_id", device.DeviceID).Msg("update device last seen failed")
	}

	return device, nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Server) sendHelloOK(ctx context.Context, conn *websocket.Conn, vip netip.Addr) error {
	resp := map[string]interface{}{
		"type":         "hello_ok",
		"virtual_ip":   vip.String(),
		"overlay_cidr": s.cfg.OverlayCIDR,
		"mtu":          s.cfg.ServerTUN.MTU,
		"routes":       []string{s.cfg.OverlayCIDR},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func (s *Server) registerClient(c *client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clients[c.DeviceID] = append(s.clients[c.DeviceID], c)
	s.vipMap[c.VirtualIP] = append(s.vipMap[c.VirtualIP], c)

	s.log.Info().
		Str("device_id", c.DeviceID).
		Str("vip", c.VirtualIP.String()).
		Int("total_conns", len(s.clients[c.DeviceID])).
		Msg("connection registered")
}

func (s *Server) unregisterClient(c *client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c.markClosed()
	s.clients[c.DeviceID] = removeConn(s.clients[c.DeviceID], c)
	s.vipMap[c.VirtualIP] = removeConn(s.vipMap[c.VirtualIP], c)

	if len(s.clients[c.DeviceID]) == 0 {
		delete(s.clients, c.DeviceID)
	}
	if len(s.vipMap[c.VirtualIP]) == 0 {
		delete(s.vipMap, c.VirtualIP)
	}
	s.removeTCPFlowsForClientLocked(c)

	s.log.Info().
		Str("device_id", c.DeviceID).
		Str("vip", c.VirtualIP.String()).
		Int("remaining_conns", len(s.clients[c.DeviceID])).
		Msg("connection unregistered")
}

func removeConn(list []*client, target *client) []*client {
	for i, c := range list {
		if c == target {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func selectPrimary(conns []*client) *client {
	for _, c := range conns {
		if !c.isClosed() {
			return c
		}
	}
	return nil
}

func selectBestStandby(conns []*client, primary *client) *client {
	var best *client
	bestAvail := -1
	for _, c := range conns {
		if c == primary || c.isClosed() {
			continue
		}
		avail := cap(c.WriteCh) - len(c.WriteCh)
		if avail > bestAvail {
			best = c
			bestAvail = avail
		}
	}
	return best
}

// serverHeartbeat sends periodic pings to detect dead clients.
func (s *Server) serverHeartbeat(ctx context.Context, c *client) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.expireTCPFlows()
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			c.writeMu.Lock()
			err := c.Conn.Ping(pingCtx)
			c.writeMu.Unlock()
			cancel()
			if err != nil {
				s.log.Warn().Err(err).Str("device_id", c.DeviceID).Msg("client ping failed")
				c.markClosed()
				c.Conn.Close(websocket.StatusNormalClosure, "ping timeout")
				return
			}
		}
	}
}

func (s *Server) readLoop(ctx context.Context, c *client) {
	for {
		msgType, data, err := c.Conn.Read(ctx)
		if err != nil {
			c.markClosed()
			s.log.Debug().Err(err).Str("device_id", c.DeviceID).Msg("read loop ended")
			return
		}

		if msgType == websocket.MessageText {
			s.log.Debug().Str("device_id", c.DeviceID).RawJSON("msg", data).Msg("control msg")
			continue
		}

		// Binary: raw IP packet.
		pkt, err := packet.ParseIPv4(data)
		if err != nil {
			s.log.Debug().Err(err).Str("device_id", c.DeviceID).Msg("invalid packet")
			continue
		}

		// Security: source IP must match allocated VIP.
		if pkt.SrcAddr != c.VirtualIP {
			s.log.Warn().
				Str("device_id", c.DeviceID).
				Str("expected", c.VirtualIP.String()).
				Str("got", pkt.SrcAddr.String()).
				Msg("source IP spoofed, dropping")
			continue
		}

		s.forwardPacket(ctx, c, pkt, data)
	}
}

func (s *Server) forwardPacket(ctx context.Context, from *client, pkt *packet.Packet, raw []byte) {
	dst := pkt.DstAddr

	// Check overlay: is destination in our CIDR?
	if s.overlayCIDR.Contains(dst.AsSlice()) {
		s.mu.RLock()
		conns := append([]*client(nil), s.vipMap[dst]...)
		s.mu.RUnlock()

		if len(conns) == 0 {
			s.log.Debug().
				Str("from", from.VirtualIP.String()).
				Str("dst", dst.String()).
				Msg("dst not found, dropping")
			return
		}

		if s.enqueueOverlay(ctx, conns, pkt, raw) {
			s.log.Debug().
				Str("from", from.VirtualIP.String()).
				Str("dst", dst.String()).
				Str("class", pkt.Class().String()).
				Int("bytes", len(raw)).
				Msg("forwarded")
		} else {
			s.log.Debug().
				Str("from", from.VirtualIP.String()).
				Str("dst", dst.String()).
				Str("class", pkt.Class().String()).
				Msg("forward drop")
		}
		return
	}

	// Non-overlay: exit traffic.
	if !s.cfg.Exit.Enabled {
		s.log.Debug().
			Str("from", from.VirtualIP.String()).
			Str("dst", dst.String()).
			Msg("exit disabled, dropping")
		return
	}

	s.log.Debug().
		Str("from", from.VirtualIP.String()).
		Str("dst", dst.String()).
		Msg("exit traffic (TUN not implemented yet)")
}

func (s *Server) enqueueOverlay(ctx context.Context, conns []*client, pkt *packet.Packet, raw []byte) bool {
	switch pkt.Class() {
	case packet.TrafficTCP:
		return s.enqueueTCP(ctx, conns, pkt, raw)
	case packet.TrafficUDP:
		return s.enqueueUDP(ctx, conns, raw)
	case packet.TrafficICMP, packet.TrafficOther:
		return s.enqueuePrimaryWithTimeout(ctx, conns, raw, relayICMPEnqueueWait)
	case packet.TrafficNoise:
		return false
	default:
		return s.enqueuePrimaryWithTimeout(ctx, conns, raw, relayICMPEnqueueWait)
	}
}

func (s *Server) enqueueTCP(ctx context.Context, conns []*client, pkt *packet.Packet, raw []byte) bool {
	tcp, err := pkt.TCPHeader()
	if err != nil {
		s.log.Debug().Err(err).Msg("tcp: invalid header")
		return false
	}

	timer := time.NewTimer(relayTCPEnqueueWait)
	defer timer.Stop()

	for {
		if target := s.boundTCPClient(tcp.Flow); target != nil {
			ok, closed := enqueueTCPToClient(ctx, target, raw, timer.C)
			if ok {
				s.afterTCPEnqueue(tcp, target)
				return true
			}
			if closed {
				s.removeTCPFlow(tcp.Flow)
				continue
			}
			return false
		}

		target := selectPrimary(conns)
		if target == nil {
			return false
		}
		if target.isClosed() {
			continue
		}

		if tcp.IsInitialSYN() && relayQueuePressure(target) {
			if standby := selectBestStandby(conns, target); tryEnqueueRelay(standby, raw) {
				s.afterTCPEnqueue(tcp, standby)
				return true
			}
		}

		ok, closed := enqueueTCPToClient(ctx, target, raw, timer.C)
		if ok {
			s.afterTCPEnqueue(tcp, target)
			return true
		}
		if closed {
			continue
		}
		return false
	}
}

func (s *Server) boundTCPClient(flow packet.TCPFlowKey) *client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	binding, ok := s.tcpFlows[flow]
	if !ok || binding.target == nil || binding.target.isClosed() {
		return nil
	}
	return binding.target
}

func (s *Server) afterTCPEnqueue(tcp *packet.TCPHeader, target *client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tcpFlows == nil {
		s.tcpFlows = make(map[packet.TCPFlowKey]tcpFlowBinding)
	}
	if tcp.ClosesFlow() {
		delete(s.tcpFlows, tcp.Flow)
		return
	}
	s.tcpFlows[tcp.Flow] = tcpFlowBinding{
		target: target,
		lastAt: time.Now(),
	}
}

func (s *Server) removeTCPFlow(flow packet.TCPFlowKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tcpFlows, flow)
}

func (s *Server) removeTCPFlowsForClientLocked(target *client) {
	for flow, binding := range s.tcpFlows {
		if binding.target == nil || binding.target == target {
			delete(s.tcpFlows, flow)
		}
	}
}

func (s *Server) expireTCPFlows() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.tcpFlows) == 0 {
		return
	}
	now := time.Now()
	for flow, binding := range s.tcpFlows {
		if binding.target == nil || binding.target.isClosed() || now.Sub(binding.lastAt) > relayFlowIdleTimeout {
			delete(s.tcpFlows, flow)
		}
	}
}

func enqueueTCPToClient(ctx context.Context, target *client, raw []byte, timeout <-chan time.Time) (ok bool, closed bool) {
	if target == nil || target.isClosed() {
		return false, true
	}
	select {
	case target.WriteCh <- raw:
		return true, false
	case <-target.doneCh():
		return false, true
	case <-ctx.Done():
		return false, false
	case <-timeout:
		return false, false
	}
}

func tryEnqueueRelay(target *client, raw []byte) bool {
	if target == nil || target.isClosed() {
		return false
	}
	select {
	case target.WriteCh <- raw:
		return true
	case <-target.doneCh():
		return false
	default:
		return false
	}
}

func relayQueuePressure(target *client) bool {
	if target == nil || cap(target.WriteCh) == 0 {
		return false
	}
	return float64(len(target.WriteCh))/float64(cap(target.WriteCh)) >= relayQueueHighWater
}

func (s *Server) enqueueUDP(ctx context.Context, conns []*client, raw []byte) bool {
	primary := selectPrimary(conns)
	if primary != nil {
		select {
		case primary.WriteCh <- raw:
			return true
		default:
		}
	}

	standby := selectBestStandby(conns, primary)
	if standby != nil {
		select {
		case standby.WriteCh <- raw:
			return true
		default:
		}
	}

	if primary == nil {
		return false
	}

	timer := time.NewTimer(relayUDPEnqueueWait)
	defer timer.Stop()
	if primary.isClosed() {
		return false
	}
	select {
	case primary.WriteCh <- raw:
		return true
	case <-primary.doneCh():
		return false
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (s *Server) enqueuePrimaryWithTimeout(ctx context.Context, conns []*client, raw []byte, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		target := selectPrimary(conns)
		if target == nil {
			return false
		}
		if target.isClosed() {
			continue
		}

		select {
		case target.WriteCh <- raw:
			return true
		case <-target.doneCh():
			continue
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		}
	}
}

func (s *Server) writeLoop(ctx context.Context, c *client) {
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-c.WriteCh:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			c.writeMu.Lock()
			err := c.Conn.Write(writeCtx, websocket.MessageBinary, data)
			c.writeMu.Unlock()
			cancel()
			if err != nil {
				c.markClosed()
				s.log.Debug().Err(err).Str("device_id", c.DeviceID).Msg("write failed")
				return
			}
		}
	}
}

func (c *client) isClosed() bool {
	return c == nil || c.closed.Load()
}

func (c *client) markClosed() {
	if c == nil {
		return
	}
	c.closed.Store(true)
	c.closeOnce.Do(func() {
		if c.done != nil {
			close(c.done)
		}
	})
}

func (c *client) doneCh() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.done
}
