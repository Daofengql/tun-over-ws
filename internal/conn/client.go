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

// Conn manages a client-side WebSocket connection with TUN device.
type Conn struct {
	uuid      string
	token     string
	serverURL string
	tunName   string

	conn      *websocket.Conn
	tunDev    *tun.Device
	virtualIP netip.Addr
	writeCh   chan []byte
	log       zerolog.Logger
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
		writeCh:   make(chan []byte, 256),
		log:       logger.Logger.With().Str("component", "client").Logger(),
	}
}

// Connect establishes the WebSocket connection, performs the hello handshake,
// creates a TUN device, and configures its IP.
func (c *Conn) Connect(ctx context.Context) error {
	// 1. Connect WebSocket.
	c.log.Info().Str("url", c.serverURL).Msg("connecting to server")
	conn, _, err := websocket.Dial(ctx, c.serverURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	c.conn = conn

	// 2. Hello handshake.
	if err := c.hello(ctx); err != nil {
		conn.CloseNow()
		return err
	}

	// 3. Create TUN device.
	dev, err := tun.Create(c.tunName, 1280)
	if err != nil {
		conn.CloseNow()
		return fmt.Errorf("create tun: %w", err)
	}
	c.tunDev = dev
	c.log.Info().Str("tun", dev.Name()).Msg("tun device created")

	// 4. Configure TUN IP.
	if err := dev.SetupIP(c.virtualIP.String()); err != nil {
		dev.Close()
		conn.CloseNow()
		return fmt.Errorf("setup tun ip: %w", err)
	}
	c.log.Info().Str("ip", c.virtualIP.String()).Str("tun", dev.Name()).Msg("tun configured")

	return nil
}

// VirtualIP returns the allocated virtual IP.
func (c *Conn) VirtualIP() netip.Addr {
	return c.virtualIP
}

// Run starts the TUN <-> WebSocket pump. Blocks until context is cancelled or error.
func (c *Conn) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// TUN -> WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.tunToWS(ctx)
	}()

	// WebSocket -> TUN
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.wsToTUN(ctx)
	}()

	// WebSocket write loop (drains writeCh)
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.writeLoop(ctx)
	}()

	// Wait for context cancellation.
	<-ctx.Done()

	// Cleanup: close TUN first (unblocks tunToWS read), then close WebSocket.
	if c.tunDev != nil {
		c.tunDev.CleanupIP(c.virtualIP.String())
		c.tunDev.Close()
	}
	if c.conn != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		c.conn.Close(websocket.StatusNormalClosure, "client shutdown")
		_ = closeCtx
	}

	wg.Wait()
	return nil
}

func (c *Conn) hello(ctx context.Context) error {
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

	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	_, resp, err := c.conn.Read(ctx)
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

// tunToWS reads packets from TUN and sends them to the server via WebSocket.
func (c *Conn) tunToWS(ctx context.Context) {
	buf := make([]byte, 1500)
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

		raw := make([]byte, n)
		copy(raw, buf[:n])

		// Log for debugging.
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

		c.writeCh <- raw
	}
}

// wsToTUN reads relay packets from the server and writes them to TUN.
func (c *Conn) wsToTUN(ctx context.Context) {
	for {
		msgType, data, err := c.conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Error().Err(err).Msg("ws read error")
			return
		}

		if msgType == websocket.MessageText {
			c.log.Debug().RawJSON("msg", data).Msg("control message")
			continue
		}

		// Binary: relay packet from server.
		pkt, err := packet.ParseIPv4(data)
		if err != nil {
			c.log.Debug().Err(err).Msg("ws: invalid packet")
			continue
		}

		c.log.Info().
			Str("src", pkt.SrcAddr.String()).
			Str("dst", pkt.DstAddr.String()).
			Int("bytes", len(data)).
			Msg("ws -> tun")

		_, err = c.tunDev.Write(data)
		if err != nil {
			c.log.Error().Err(err).Msg("tun write error")
		}
	}
}

func (c *Conn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-c.writeCh:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.conn.Write(writeCtx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				c.log.Error().Err(err).Msg("ws write error")
				return
			}
		}
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
