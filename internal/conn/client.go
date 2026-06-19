package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/daofeng/ws-vpn-go/internal/logger"
	"github.com/daofeng/ws-vpn-go/internal/packet"
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

// PacketCallback is called when a relay packet is received.
type PacketCallback func(src, dst netip.Addr, data []byte)

// Conn manages a client-side WebSocket connection.
type Conn struct {
	uuid      string
	token     string
	serverURL string
	mtu       int

	conn      *websocket.Conn
	virtualIP netip.Addr
	onPacket  PacketCallback
	writeCh   chan []byte
	log       zerolog.Logger
}

// New creates a new client connection.
func New(serverURL, uuid, token string, mtu int, onPacket PacketCallback) *Conn {
	return &Conn{
		uuid:      uuid,
		token:     token,
		serverURL: serverURL,
		mtu:       mtu,
		onPacket:  onPacket,
		writeCh:   make(chan []byte, 256),
		log:       logger.Logger.With().Str("component", "client").Logger(),
	}
}

// Connect establishes the WebSocket connection and performs the hello handshake.
func (c *Conn) Connect(ctx context.Context) error {
	c.log.Info().Str("url", c.serverURL).Msg("connecting to server")

	conn, _, err := websocket.Dial(ctx, c.serverURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	c.conn = conn

	if err := c.hello(ctx); err != nil {
		conn.CloseNow()
		return err
	}

	return nil
}

// Run starts the read and write loops. Blocks until context is cancelled or error.
func (c *Conn) Run(ctx context.Context) error {
	defer c.conn.CloseNow()

	go c.writeLoop(ctx)

	return c.readLoop(ctx)
}

// VirtualIP returns the allocated virtual IP.
func (c *Conn) VirtualIP() netip.Addr {
	return c.virtualIP
}

// Send queues a packet for sending.
func (c *Conn) Send(data []byte) {
	select {
	case c.writeCh <- data:
	default:
		c.log.Warn().Msg("write channel full, dropping packet")
	}
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

func (c *Conn) readLoop(ctx context.Context) error {
	for {
		msgType, data, err := c.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		if msgType == websocket.MessageText {
			// Control message from server (e.g. heartbeat ack)
			c.log.Debug().RawJSON("msg", data).Msg("control message")
			continue
		}

		// Binary: raw IP packet
		pkt, err := packet.ParseIPv4(data)
		if err != nil {
			c.log.Debug().Err(err).Msg("invalid packet from server")
			continue
		}

		if c.onPacket != nil {
			c.onPacket(pkt.SrcAddr, pkt.DstAddr, data)
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
				c.log.Error().Err(err).Msg("write failed")
				return
			}
		}
	}
}

func hostname() string {
	h, _ := getHostname()
	return h
}
