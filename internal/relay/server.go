package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/Daofengql/tun-over-ws/internal/config"
	"github.com/Daofengql/tun-over-ws/internal/logger"
	"github.com/Daofengql/tun-over-ws/internal/packet"
)

const pingInterval = 30 * time.Second

// client represents a connected client.
type client struct {
	UUID       string
	VirtualIP  netip.Addr
	Conn       *websocket.Conn
	WriteCh    chan []byte
	RemoteAddr string
}

// Server is the WebSocket relay server.
type Server struct {
	cfg       *config.ServerConfig
	allocator *VIPAllocator

	mu      sync.RWMutex
	clients map[string]*client     // UUID -> client
	vipMap  map[netip.Addr]*client // VIP -> client

	overlayCIDR *net.IPNet // pre-parsed for fast matching

	log zerolog.Logger
}

// NewServer creates a new relay server.
func NewServer(cfg *config.ServerConfig) (*Server, error) {
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
		allocator:   alloc,
		clients:     make(map[string]*client),
		vipMap:      make(map[netip.Addr]*client),
		overlayCIDR: cidr,
		log:         logger.Logger.With().Str("component", "relay").Logger(),
	}, nil
}

// ListenAndServe starts the HTTP server and listens for WebSocket connections.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		s.handleTunnel(ctx, w, r)
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

func (s *Server) handleTunnel(ctx context.Context, w http.ResponseWriter, r *http.Request) {
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
	// Step 1: Read hello message.
	helloCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	uuid, err := s.readHello(helloCtx, conn)
	cancel()
	if err != nil {
		s.log.Warn().Err(err).Str("remote", remoteAddr).Msg("hello failed")
		conn.Close(websocket.StatusPolicyViolation, "hello failed")
		return
	}

	// Step 2: Allocate VIP.
	vip, err := s.allocator.Allocate(uuid)
	if err != nil {
		s.log.Error().Err(err).Str("uuid", uuid).Msg("vip allocation failed")
		conn.Close(websocket.StatusInternalError, "vip allocation failed")
		return
	}

	// Step 3: Send hello_ok.
	if err := s.sendHelloOK(ctx, conn, vip); err != nil {
		s.log.Error().Err(err).Str("uuid", uuid).Msg("send hello_ok failed")
		s.allocator.Release(uuid)
		conn.Close(websocket.StatusInternalError, "hello_ok failed")
		return
	}

	// Step 4: Register client (replace existing if same UUID).
	c := &client{
		UUID:       uuid,
		VirtualIP:  vip,
		Conn:       conn,
		WriteCh:    make(chan []byte, 512),
		RemoteAddr: remoteAddr,
	}
	s.registerClient(c)
	defer s.unregisterClient(c)

	s.log.Info().
		Str("uuid", uuid).
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

func (s *Server) readHello(ctx context.Context, conn *websocket.Conn) (string, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	var hello struct {
		Type  string `json:"type"`
		UUID  string `json:"uuid"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &hello); err != nil {
		return "", fmt.Errorf("parse hello: %w", err)
	}
	if hello.Type != "hello" {
		return "", fmt.Errorf("expected hello, got %q", hello.Type)
	}
	if !s.validateToken(hello.Token) {
		return "", fmt.Errorf("invalid token")
	}
	if hello.UUID == "" {
		return "", fmt.Errorf("empty uuid")
	}
	return hello.UUID, nil
}

func (s *Server) validateToken(token string) bool {
	for _, t := range s.cfg.Auth.Tokens {
		if t == token {
			return true
		}
	}
	return false
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

	// If same UUID reconnects, close old connection.
	if old, ok := s.clients[c.UUID]; ok {
		s.log.Info().Str("uuid", c.UUID).Msg("replacing existing connection")
		old.Conn.Close(websocket.StatusNormalClosure, "replaced by new connection")
		delete(s.vipMap, old.VirtualIP)
	}

	s.clients[c.UUID] = c
	s.vipMap[c.VirtualIP] = c
}

func (s *Server) unregisterClient(c *client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.clients[c.UUID]
	if ok && current == c {
		delete(s.clients, c.UUID)
		delete(s.vipMap, c.VirtualIP)
		s.log.Info().
			Str("uuid", c.UUID).
			Str("vip", c.VirtualIP.String()).
			Msg("client disconnected")
	}
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
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Conn.Ping(pingCtx)
			cancel()
			if err != nil {
				s.log.Warn().Err(err).Str("uuid", c.UUID).Msg("client ping failed")
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
			s.log.Debug().Err(err).Str("uuid", c.UUID).Msg("read loop ended")
			return
		}

		if msgType == websocket.MessageText {
			s.log.Debug().Str("uuid", c.UUID).RawJSON("msg", data).Msg("control msg")
			continue
		}

		// Binary: raw IP packet.
		pkt, err := packet.ParseIPv4(data)
		if err != nil {
			s.log.Debug().Err(err).Str("uuid", c.UUID).Msg("invalid packet")
			continue
		}

		// Security: source IP must match allocated VIP.
		if pkt.SrcAddr != c.VirtualIP {
			s.log.Warn().
				Str("uuid", c.UUID).
				Str("expected", c.VirtualIP.String()).
				Str("got", pkt.SrcAddr.String()).
				Msg("source IP spoofed, dropping")
			continue
		}

		s.forwardPacket(c, pkt, data)
	}
}

func (s *Server) forwardPacket(from *client, pkt *packet.Packet, raw []byte) {
	dst := pkt.DstAddr

	// Check overlay: is destination in our CIDR?
	if s.overlayCIDR.Contains(dst.AsSlice()) {
		s.mu.RLock()
		target, ok := s.vipMap[dst]
		s.mu.RUnlock()

		if !ok {
			s.log.Debug().
				Str("from", from.VirtualIP.String()).
				Str("dst", dst.String()).
				Msg("dst not found, dropping")
			return
		}

		select {
		case target.WriteCh <- raw:
			s.log.Debug().
				Str("from", from.VirtualIP.String()).
				Str("dst", dst.String()).
				Int("bytes", len(raw)).
				Msg("forwarded")
		default:
			s.log.Warn().
				Str("dst", dst.String()).
				Msg("target write channel full, dropping")
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
			err := c.Conn.Write(writeCtx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				s.log.Debug().Err(err).Str("uuid", c.UUID).Msg("write failed")
				return
			}
		}
	}
}
