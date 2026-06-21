package config

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ServerConfig is the server configuration.
type ServerConfig struct {
	Listen      string        `yaml:"listen"`
	OverlayCIDR string        `yaml:"overlay_cidr"`
	ServerTUN   TUNConfig     `yaml:"server_tun"`
	Exit        ExitConfig    `yaml:"exit"`
	Heartbeat   HeartbeatConf `yaml:"heartbeat"`
	Database    DatabaseConf  `yaml:"database"`
	Admin       AdminConf     `yaml:"admin"`
}

// DatabaseConf is database configuration.
type DatabaseConf struct {
	Driver string `yaml:"driver"` // sqlite or mysql
	DSN    string `yaml:"dsn"`
}

// AdminConf is admin console configuration.
type AdminConf struct {
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
	JWTSecret string `yaml:"jwt_secret"`
	StaticDir string `yaml:"static_dir"`
	DevOrigin string `yaml:"dev_origin"`
}

// TUNConfig is TUN device configuration.
type TUNConfig struct {
	Enabled bool   `yaml:"enabled"`
	Name    string `yaml:"name"`
	IP      string `yaml:"ip"`
	MTU     int    `yaml:"mtu"`
}

// ExitConfig is exit gateway configuration.
type ExitConfig struct {
	Enabled bool `yaml:"enabled"`
}

// HeartbeatConf is heartbeat configuration.
type HeartbeatConf struct {
	Interval time.Duration `yaml:"interval"`
}

// UnmarshalYAML implements custom YAML unmarshaling for HeartbeatConf.
func (h *HeartbeatConf) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Interval string `yaml:"interval"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	if raw.Interval != "" {
		d, err := time.ParseDuration(raw.Interval)
		if err != nil {
			return fmt.Errorf("invalid heartbeat interval: %w", err)
		}
		h.Interval = d
	}
	return nil
}

// ClientConfig is the client configuration.
type ClientConfig struct {
	ServerURL string       `yaml:"server_url"`
	DeviceDir string       `yaml:"device_dir"` // directory to store device credentials
	TUN       TUNConfig    `yaml:"tun"`
	Routes    RoutesConfig `yaml:"routes"`
}

// RoutesConfig is client routing configuration.
type RoutesConfig struct {
	Exit ExitRouteConfig `yaml:"exit"`
}

// ExitRouteConfig is exit route configuration.
type ExitRouteConfig struct {
	Enabled bool `yaml:"enabled"`
}

// LoadServerConfig loads server config from a YAML file.
func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := DefaultServerConfig()
	if err := decodeYAMLStrict(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadClientConfig loads client config from a YAML file.
func LoadClientConfig(path string) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := DefaultClientConfig()
	if err := decodeYAMLStrict(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func decodeYAMLStrict(data []byte, out interface{}) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(out)
}

// DefaultClientConfig returns default client configuration.
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		DeviceDir: "~/.wsvpn",
		TUN: TUNConfig{
			Name: "wsvpn0",
			MTU:  1280,
		},
		Routes: RoutesConfig{
			Exit: ExitRouteConfig{Enabled: false},
		},
	}
}

// DefaultServerConfig returns default server configuration.
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Listen:      ":8443",
		OverlayCIDR: "10.66.0.0/24",
		ServerTUN: TUNConfig{
			Enabled: true,
			Name:    "wsvpn0",
			IP:      "10.66.0.1",
			MTU:     1280,
		},
		Exit: ExitConfig{Enabled: false},
		Heartbeat: HeartbeatConf{
			Interval: 30 * time.Second,
		},
		Database: DatabaseConf{
			Driver: "sqlite",
			DSN:    "./data/wsvpn.db",
		},
	}
}

// Validate checks server configuration.
func (c *ServerConfig) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen address is required")
	}
	prefix, err := netip.ParsePrefix(c.OverlayCIDR)
	if err != nil {
		return fmt.Errorf("invalid overlay_cidr %q: %w", c.OverlayCIDR, err)
	}
	if !prefix.Addr().Is4() {
		return fmt.Errorf("overlay_cidr must be IPv4, got %q", c.OverlayCIDR)
	}
	serverIP, err := netip.ParseAddr(c.ServerTUN.IP)
	if err != nil {
		return fmt.Errorf("invalid server_tun.ip %q: %w", c.ServerTUN.IP, err)
	}
	if !serverIP.Is4() {
		return fmt.Errorf("server_tun.ip must be IPv4, got %q", c.ServerTUN.IP)
	}
	if !prefix.Contains(serverIP) {
		return fmt.Errorf("server_tun.ip %q must be inside overlay_cidr %q", c.ServerTUN.IP, c.OverlayCIDR)
	}
	if c.ServerTUN.MTU <= 0 {
		c.ServerTUN.MTU = 1280
	}
	if c.Heartbeat.Interval <= 0 {
		c.Heartbeat.Interval = 30 * time.Second
	}
	if c.Database.Driver == "" {
		c.Database.Driver = "sqlite"
	}
	if !strings.EqualFold(c.Database.Driver, "sqlite") {
		return fmt.Errorf("unsupported database.driver %q", c.Database.Driver)
	}
	if c.Database.DSN == "" {
		c.Database.DSN = "./data/wsvpn.db"
	}
	return nil
}

// Validate checks client configuration.
func (c *ClientConfig) Validate() error {
	if c.ServerURL == "" {
		return fmt.Errorf("server_url is required")
	}
	if c.DeviceDir == "" {
		c.DeviceDir = "~/.wsvpn"
	}
	if c.TUN.MTU <= 0 {
		c.TUN.MTU = 1280
	}
	return nil
}
