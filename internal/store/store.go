package store

import (
	"context"
	"time"
)

// DeviceStatus represents the approval status of a device.
type DeviceStatus string

const (
	DeviceStatusPending  DeviceStatus = "pending"
	DeviceStatusApproved DeviceStatus = "approved"
	DeviceStatusRevoked  DeviceStatus = "revoked"
)

// Device represents a VPN client device.
type Device struct {
	ID           int64        `json:"id"`
	DeviceID     string       `json:"device_id"`
	DeviceInfo   string       `json:"device_info"` // JSON string
	Name         string       `json:"name"`
	VirtualIP    *string      `json:"virtual_ip"` // manual override, nil = use auto
	AutoVIP      string       `json:"auto_vip"`
	Status       DeviceStatus `json:"status"`
	AccessKey    *string      `json:"-"` // stored as hash
	RefreshKey   *string      `json:"-"` // stored as hash
	KeyExpiresAt *time.Time   `json:"key_expires_at"`
	RKExpiresAt  *time.Time   `json:"rk_expires_at"`
	ApprovedBy   *int64       `json:"approved_by"`
	LastSeenAt   *time.Time   `json:"last_seen_at"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// EffectiveIP returns the manual override VIP if set, otherwise the auto-assigned VIP.
func (d *Device) EffectiveIP() string {
	if d.VirtualIP != nil && *d.VirtualIP != "" {
		return *d.VirtualIP
	}
	return d.AutoVIP
}

// AuthSession represents a pending device authorization session.
type AuthSession struct {
	ID          int64     `json:"id"`
	SessionCode string    `json:"session_code"`
	DeviceID    string    `json:"device_id"`
	Status      string    `json:"status"` // pending / approved / expired
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// Admin represents a web console administrator.
type Admin struct {
	ID        int64      `json:"id"`
	Username  string     `json:"username"`
	Password  string     `json:"-"` // bcrypt hash
	CreatedAt time.Time  `json:"created_at"`
	LastLogin *time.Time `json:"last_login"`
}

// Store defines the interface for persistent storage.
type Store interface {
	// Device operations
	CreateDevice(ctx context.Context, d *Device) error
	GetDeviceByID(ctx context.Context, deviceID string) (*Device, error)
	GetDeviceByAK(ctx context.Context, ak string) (*Device, error)
	GetDeviceByRK(ctx context.Context, rk string) (*Device, error)
	UpdateDevice(ctx context.Context, deviceID string, d *Device) error
	DeleteDevice(ctx context.Context, deviceID string) error
	ListDevices(ctx context.Context) ([]*Device, error)
	UpdateDeviceLastSeen(ctx context.Context, deviceID string) error
	UpdateDeviceKeys(ctx context.Context, deviceID string, akHash, rkHash string, akExpiry, rkExpiry time.Time) error
	GetDeviceByVIP(ctx context.Context, vip string) (*Device, error)

	// Auth session operations
	CreateAuthSession(ctx context.Context, s *AuthSession) error
	GetAuthSession(ctx context.Context, code string) (*AuthSession, error)
	UpdateAuthSessionStatus(ctx context.Context, code string, status string) error
	CleanExpiredAuthSessions(ctx context.Context) error

	// Admin operations
	CreateAdmin(ctx context.Context, a *Admin) error
	GetAdminByUsername(ctx context.Context, username string) (*Admin, error)
	UpdateAdminLastLogin(ctx context.Context, id int64) error

	// VIP allocation
	GetAllocatedVIPs(ctx context.Context) (map[string]string, error) // vip -> device_id

	// Lifecycle
	Close() error
}
