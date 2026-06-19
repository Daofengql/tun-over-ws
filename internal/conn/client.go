package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/daofeng/ws-vpn-go/internal/logger"
	"github.com/daofeng/ws-vpn-go/internal/packet"
	"github.com/daofeng/ws-vpn-go/internal/tun"
)

const (
	pingInterval = 30 * time.Second
	readTimeout  = 90 * time.Second
	writeTimeout = 5 * time.Second

	reconnectBaseDelay = time.Second
	reconnectMaxDelay  = 30 * time.Second
)

// HelloMessage is the initial client registration message.
type HelloMessage struct {
	Type     string `json:"type"`
	UUID     string `json:"uuid"`
	Token    string `json:"token"`
	Hostname string `json:"hostname"`
	WantExit bool   `json:"want_exit"`
}

// HelloOK is the server response with allocated VIP.
type HelloOK struct {
	Type        string   `json:"type"`
	VirtualIP   string   `json:"virtual_ip"`
	OverlayCIDR string   `json:"overlay_cidr"`
	MTU         int      `json:"mtu"`
	Routes      []string `json:"routes"`
}

// bufPool reuses packet buffers to reduce GC pressure.
var bufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 1500)
		return &b
	},
}

func getBuf() *[]byte  { return bufPool.Get().(*[]byte) }
func putBuf(b *[]byte) { bufPool.Put(b) }

// Conn manages a client-side WebSocket connection with TUN device.
type Conn struct {
	uuid      string
	token     string
	serverURL string
	tunName   string

	// TUN lives for the lifetime of Conn (not recreated on reconnect).
	tunDev    *tun.Device
	virtualIP netip.Addr

	// These are reset on each connection cycle.
	wsConn  *websocket.Conn
	writeCh chan []byte

	log zerolog.Logger
}

// New creates a new client connection.
func New(serverURL, uuid, token, tunName string) *Conn {
	if tunName == "" {
		tunName = "wsvpn0"
	}
	return &Conn{
		uuid:      uuid,
		token:     token,
		serverURL: serverURL,
		tunName:   tunName,
		writeCh:   make(chan []byte, 512),
		log:       logger.Logger.With().Str("component", "client").Logger(),
	}
}

// Connect establishes the WebSocket connection, performs the hello handshake,
// creates a TUN device, and configures its IP. Blocks until connected.
func (c *Conn) Connect(ctx context.Context) error {
	if err := c.connectOnce(ctx); err != nil {
		return err
	}

	// Create TUN on first successful connection.
	if c.tunDev == nil {
		dev, err := tun.Create(c.tunName, 1280)
		if err != nil {
			c.wsConn.CloseNow()
			return fmt.Errorf("create tun: %w", err)
		}
		c.tunDev = dev
		c.log.Info().Str("tun", dev.Name()).Msg("tun device created")

		if err := dev.SetupIP(c.virtualIP.String()); err != nil {
			dev.Close()
			c.wsConn.CloseNow()
			return fmt.Errorf("setup tun ip: %w", err)
		}
		c.log.Info().Str("ip", c.virtualIP.String()).Str("tun", dev.Name()).Msg("tun configured")
	}
	return nil
}

// VirtualIP returns the allocated virtual IP.
func (c *Conn) VirtualIP() netip.Addr {
	return c.virtualIP
}

// Run starts the TUN <-> WebSocket pump with automatic reconnection.
// Blocks until ctx is cancelled.
func (c *Conn) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// TUN -> WebSocket: survives reconnections.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		c.tunToWS(ctx)
	}()

	// Connection loop: reconnect on failure.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		c.connLoop(ctx)
	}()

	<-ctx.Done()

	// Cleanup.
	if c.tunDev != nil {
		c.tunDev.CleanupIP(c.virtualIP.String())
		c.tunDev.Close()
	}
	c.closeWS()

	wg.Wait()
	return nil
}

// connectOnce performs a single WebSocket connection + hello handshake.
func (c *Conn) connectOnce(ctx context.Context) error {
	c.log.Info().Str("url", c.serverURL).Msg("connecting to server")

	wsConn, _, err := websocket.Dial(ctx, c.serverURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}

	if err := c.hello(ctx, wsConn); err != nil {
		wsConn.CloseNow()
		return err
	}

	c.wsConn = wsConn
	return nil
}

// connLoop handles the connection lifecycle with reconnection.
func (c *Conn) connLoop(ctx context.Context) {
	delay := reconnectBaseDelay

	for {
		if c.wsConn == nil {
			c.log.Warn().Dur("delay", delay).Msg("reconnecting...")
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}

			if err := c.connectOnce(ctx); err != nil {
				c.log.Error().Err(err).Msg("reconnect failed")
				delay = min(delay*2, reconnectMaxDelay)
				continue
			}
			// Reset delay on success.
			delay = reconnectBaseDelay
			c.log.Info().Str("vip", c.virtualIP.String()).Msg("reconnected")
		}

		// Run the current connection.
		c.runConn(ctx)
		if ctx.Err() != nil {
			return
		}
		c.closeWS()
		// Connection lost, loop will reconnect.
	}
}

// runConn runs read/write/heartbeat for a single connection.
func (c *Conn) runConn(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// Read from server, write to TUN.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		c.readLoop(ctx)
	}()

	// Drain writeCh to server.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		c.writeLoop(ctx)
	}()

	// Heartbeat: send Ping every 30s.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		c.heartbeat(ctx)
	}()

	wg.Wait()
}

func (c *Conn) hello(ctx context.Context, wsConn *websocket.Conn) error {
	hello := HelloMessage{
		Type:     "hello",
		UUID:     c.uuid,
		Token:    c.token,
		Hostname: hostname(),
		WantExit: false,
	}

	data, err := json.Marshal(hello)
	if err != nil {
		return fmt.Errorf("marshal hello: %w", err)
	}

	if err := wsConn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	_, resp, err := wsConn.Read(ctx)
	if err != nil {
		return fmt.Errorf("read hello_ok: %w", err)
	}

	var ok HelloOK
	if err := json.Unmarshal(resp, &ok); err != nil {
		return fmt.Errorf("parse hello_ok: %w", err)
	}
	if ok.Type != "hello_ok" {
		return fmt.Errorf("unexpected response type: %s", ok.Type)
	}

	ip, err := netip.ParseAddr(ok.VirtualIP)
	if err != nil {
		return fmt.Errorf("invalid virtual_ip in hello_ok: %w", err)
	}
	c.virtualIP = ip

	c.log.Info().
		Str("virtual_ip", c.virtualIP.String()).
		Str("overlay_cidr", ok.OverlayCIDR).
		Int("mtu", ok.MTU).
		Msg("registered with server")

	return nil
}

// heartbeat sends WebSocket pings periodically.
func (c *Conn) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.wsConn.Ping(pingCtx)
			cancel()
			if err != nil {
				c.log.Warn().Err(err).Msg("heartbeat failed")
				return
			}
			c.log.Debug().Msg("heartbeat ok")
		}
	}
}

// readLoop reads packets from server and writes to TUN.
func (c *Conn) readLoop(ctx context.Context) {
	for {
		// Use per-read timeout to detect dead connections.
		readCtx, cancel := context.WithTimeout(ctx, readTimeout)
		msgType, data, err := c.wsConn.Read(readCtx)
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Warn().Err(err).Msg("read failed, connection lost")
			return
		}

		if msgType == websocket.MessageText {
			c.log.Debug().RawJSON("msg", data).Msg("control message")
			continue
		}

		// Binary: relay packet from server -> TUN.
		pkt, err := packet.ParseIPv4(data)
		if err != nil {
			c.log.Debug().Err(err).Msg("invalid packet from server")
			continue
		}

		c.log.Debug().
			Str("src", pkt.SrcAddr.String()).
			Str("dst", pkt.DstAddr.String()).
			Int("bytes", len(data)).
			Msg("ws -> tun")

		if _, err := c.tunDev.Write(data); err != nil {
			c.log.Error().Err(err).Msg("tun write failed")
		}
	}
}

// writeLoop drains writeCh and sends packets to the server.
func (c *Conn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-c.writeCh:
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.wsConn.Write(writeCtx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				c.log.Error().Err(err).Msg("write failed")
				return
			}
		}
	}
}

// tunToWS reads packets from TUN and queues them for the server.
// Survives reconnections — uses a fresh writeCh on each cycle.
func (c *Conn) tunToWS(ctx context.Context) {
	bufp := getBuf()
	defer putBuf(bufp)
	buf := *bufp

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := c.tunDev.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Debug().Err(err).Msg("tun read error")
			continue
		}
		if n == 0 {
			continue
		}

		// Copy packet data — buf will be reused.
		raw := make([]byte, n)
		copy(raw, buf[:n])

		pkt, err := packet.ParseIPv4(raw)
		if err != nil {
			c.log.Debug().Err(err).Msg("tun: invalid packet")
			continue
		}
		c.log.Debug().
			Str("src", pkt.SrcAddr.String()).
			Str("dst", pkt.DstAddr.String()).
			Int("bytes", n).
			Msg("tun -> ws")

		select {
		case c.writeCh <- raw:
		default:
			c.log.Warn().Msg("write channel full, dropping packet")
		}
	}
}

func (c *Conn) closeWS() {
	if c.wsConn != nil {
		c.wsConn.Close(websocket.StatusNormalClosure, "reconnecting")
		c.wsConn = nil
	}
	// Drain writeCh for the old connection.
	for {
		select {
		case <-c.writeCh:
		default:
			return
		}
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
