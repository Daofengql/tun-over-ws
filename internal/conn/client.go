package conn

import (
	"context"
	"net/netip"
	"os"

	"github.com/Daofengql/tun-over-ws/internal/tun"
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

// Conn preserves the old single-client API while delegating to Pool.
// All client traffic now goes through the primary/standby pool implementation.
type Conn struct {
	pool *Pool
}

// New creates a backward-compatible client wrapper.
func New(serverURL, uuid, token, tunName string) *Conn {
	return &Conn{
		pool: NewPool(serverURL, uuid, token, tunName, tun.DefaultMTU),
	}
}

// Connect establishes the pool, creates TUN, and configures its IP.
func (c *Conn) Connect(ctx context.Context) error {
	return c.pool.Connect(ctx)
}

// VirtualIP returns the allocated virtual IP.
func (c *Conn) VirtualIP() netip.Addr {
	return c.pool.VirtualIP()
}

// Run starts the TUN <-> WebSocket pool pump.
func (c *Conn) Run(ctx context.Context) error {
	return c.pool.Run(ctx)
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
