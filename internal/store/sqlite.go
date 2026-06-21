package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements Store using SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens or creates a SQLite database at the given path.
func NewSQLiteStore(dsn string) (*SQLiteStore, error) {
	if err := ensureSQLiteDir(dsn); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Enable WAL mode for better concurrent read performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func ensureSQLiteDir(dsn string) error {
	if dsn == "" || dsn == ":memory:" || strings.HasPrefix(dsn, "file:") {
		return nil
	}
	dir := filepath.Dir(dsn)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create sqlite directory: %w", err)
	}
	return nil
}

func (s *SQLiteStore) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS admins (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			username   TEXT NOT NULL UNIQUE,
			password   TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_login DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS devices (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			device_id       TEXT NOT NULL UNIQUE,
			device_info     TEXT NOT NULL,
			name            TEXT DEFAULT '',
			virtual_ip      TEXT DEFAULT NULL,
			auto_vip        TEXT NOT NULL,
			status          TEXT DEFAULT 'pending',
			access_key      TEXT DEFAULT NULL,
			refresh_key     TEXT DEFAULT NULL,
			key_expires_at  DATETIME,
			rk_expires_at   DATETIME,
			approved_by     INTEGER REFERENCES admins(id),
			last_seen_at    DATETIME,
			created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS auth_sessions (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			session_code TEXT NOT NULL UNIQUE,
			device_id    TEXT NOT NULL,
			status       TEXT DEFAULT 'pending',
			expires_at   DATETIME NOT NULL,
			created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_sessions_code ON auth_sessions(session_code)`,
		`CREATE INDEX IF NOT EXISTS idx_devices_device_id ON devices(device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_devices_ak ON devices(access_key)`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// ───────────────────────── Device Operations ─────────────────────────

func (s *SQLiteStore) CreateDevice(ctx context.Context, d *Device) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO devices (device_id, device_info, name, virtual_ip, auto_vip, status, access_key, refresh_key, key_expires_at, rk_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.DeviceID, d.DeviceInfo, d.Name, d.VirtualIP, d.AutoVIP, d.Status,
		d.AccessKey, d.RefreshKey, d.KeyExpiresAt, d.RKExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	id, _ := res.LastInsertId()
	d.ID = id
	return nil
}

func (s *SQLiteStore) GetDeviceByID(ctx context.Context, deviceID string) (*Device, error) {
	d := &Device{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, device_id, device_info, name, virtual_ip, auto_vip, status,
		        access_key, refresh_key, key_expires_at, rk_expires_at,
		        approved_by, last_seen_at, created_at, updated_at
		 FROM devices WHERE device_id = ?`, deviceID,
	).Scan(
		&d.ID, &d.DeviceID, &d.DeviceInfo, &d.Name, &d.VirtualIP, &d.AutoVIP, &d.Status,
		&d.AccessKey, &d.RefreshKey, &d.KeyExpiresAt, &d.RKExpiresAt,
		&d.ApprovedBy, &d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device by id: %w", err)
	}
	return d, nil
}

func (s *SQLiteStore) GetDeviceByAK(ctx context.Context, ak string) (*Device, error) {
	return s.getDeviceByKey(ctx, "access_key", ak)
}

func (s *SQLiteStore) GetDeviceByRK(ctx context.Context, rk string) (*Device, error) {
	return s.getDeviceByKey(ctx, "refresh_key", rk)
}

func (s *SQLiteStore) getDeviceByKey(ctx context.Context, column, value string) (*Device, error) {
	d := &Device{}
	err := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT id, device_id, device_info, name, virtual_ip, auto_vip, status,
		        access_key, refresh_key, key_expires_at, rk_expires_at,
		        approved_by, last_seen_at, created_at, updated_at
		 FROM devices WHERE %s = ?`, column), value,
	).Scan(
		&d.ID, &d.DeviceID, &d.DeviceInfo, &d.Name, &d.VirtualIP, &d.AutoVIP, &d.Status,
		&d.AccessKey, &d.RefreshKey, &d.KeyExpiresAt, &d.RKExpiresAt,
		&d.ApprovedBy, &d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device by %s: %w", column, err)
	}
	return d, nil
}

func (s *SQLiteStore) UpdateDevice(ctx context.Context, deviceID string, d *Device) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET name = ?, virtual_ip = ?, status = ?, approved_by = ?, updated_at = ? WHERE device_id = ?`,
		d.Name, d.VirtualIP, d.Status, d.ApprovedBy, time.Now(), deviceID,
	)
	if err != nil {
		return fmt.Errorf("update device: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteDevice(ctx context.Context, deviceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE device_id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("delete device: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListDevices(ctx context.Context) ([]*Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, device_id, device_info, name, virtual_ip, auto_vip, status,
		        access_key, refresh_key, key_expires_at, rk_expires_at,
		        approved_by, last_seen_at, created_at, updated_at
		 FROM devices ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	var devices []*Device
	for rows.Next() {
		d := &Device{}
		err := rows.Scan(
			&d.ID, &d.DeviceID, &d.DeviceInfo, &d.Name, &d.VirtualIP, &d.AutoVIP, &d.Status,
			&d.AccessKey, &d.RefreshKey, &d.KeyExpiresAt, &d.RKExpiresAt,
			&d.ApprovedBy, &d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (s *SQLiteStore) UpdateDeviceLastSeen(ctx context.Context, deviceID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ? WHERE device_id = ?`,
		time.Now(), deviceID,
	)
	return err
}

func (s *SQLiteStore) UpdateDeviceKeys(ctx context.Context, deviceID string, akHash, rkHash string, akExpiry, rkExpiry time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET access_key = ?, refresh_key = ?, key_expires_at = ?, rk_expires_at = ?, status = 'approved', updated_at = ? WHERE device_id = ?`,
		akHash, rkHash, akExpiry, rkExpiry, time.Now(), deviceID,
	)
	return err
}

func (s *SQLiteStore) GetDeviceByVIP(ctx context.Context, vip string) (*Device, error) {
	d := &Device{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, device_id, device_info, name, virtual_ip, auto_vip, status,
		        access_key, refresh_key, key_expires_at, rk_expires_at,
		        approved_by, last_seen_at, created_at, updated_at
		 FROM devices WHERE virtual_ip = ? OR (virtual_ip IS NULL AND auto_vip = ?)`, vip, vip,
	).Scan(
		&d.ID, &d.DeviceID, &d.DeviceInfo, &d.Name, &d.VirtualIP, &d.AutoVIP, &d.Status,
		&d.AccessKey, &d.RefreshKey, &d.KeyExpiresAt, &d.RKExpiresAt,
		&d.ApprovedBy, &d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device by vip: %w", err)
	}
	return d, nil
}

// ───────────────────────── Auth Session Operations ─────────────────────────

func (s *SQLiteStore) CreateAuthSession(ctx context.Context, as *AuthSession) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_sessions (session_code, device_id, status, expires_at) VALUES (?, ?, ?, ?)`,
		as.SessionCode, as.DeviceID, as.Status, as.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("create auth session: %w", err)
	}
	id, _ := res.LastInsertId()
	as.ID = id
	return nil
}

func (s *SQLiteStore) GetAuthSession(ctx context.Context, code string) (*AuthSession, error) {
	as := &AuthSession{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, session_code, device_id, status, expires_at, created_at FROM auth_sessions WHERE session_code = ?`, code,
	).Scan(&as.ID, &as.SessionCode, &as.DeviceID, &as.Status, &as.ExpiresAt, &as.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get auth session: %w", err)
	}
	return as, nil
}

func (s *SQLiteStore) UpdateAuthSessionStatus(ctx context.Context, code string, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE auth_sessions SET status = ? WHERE session_code = ?`,
		status, code,
	)
	return err
}

func (s *SQLiteStore) CleanExpiredAuthSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE expires_at < ? OR status != 'pending'`,
		time.Now(),
	)
	return err
}

// ───────────────────────── Admin Operations ─────────────────────────

func (s *SQLiteStore) CreateAdmin(ctx context.Context, a *Admin) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO admins (username, password) VALUES (?, ?)`,
		a.Username, a.Password,
	)
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	id, _ := res.LastInsertId()
	a.ID = id
	return nil
}

func (s *SQLiteStore) GetAdminByUsername(ctx context.Context, username string) (*Admin, error) {
	a := &Admin{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password, created_at, last_login FROM admins WHERE username = ?`, username,
	).Scan(&a.ID, &a.Username, &a.Password, &a.CreatedAt, &a.LastLogin)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get admin by username: %w", err)
	}
	return a, nil
}

func (s *SQLiteStore) UpdateAdminLastLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE admins SET last_login = ? WHERE id = ?`,
		time.Now(), id,
	)
	return err
}

// ───────────────────────── VIP Allocation ─────────────────────────

func (s *SQLiteStore) GetAllocatedVIPs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(virtual_ip, auto_vip) as vip, device_id FROM devices WHERE status != 'revoked'`,
	)
	if err != nil {
		return nil, fmt.Errorf("get allocated vips: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var vip, deviceID string
		if err := rows.Scan(&vip, &deviceID); err != nil {
			return nil, fmt.Errorf("scan vip: %w", err)
		}
		result[vip] = deviceID
	}
	return result, rows.Err()
}

// EnsureAdmin creates the initial admin user if it doesn't exist.
func (s *SQLiteStore) EnsureAdmin(ctx context.Context, username, passwordHash string) error {
	existing, err := s.GetAdminByUsername(ctx, username)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil // already exists
	}
	return s.CreateAdmin(ctx, &Admin{
		Username: username,
		Password: passwordHash,
	})
}

// DeviceInfoJSON helper to unmarshal device info
func DeviceInfoMap(info string) map[string]interface{} {
	var m map[string]interface{}
	json.Unmarshal([]byte(info), &m)
	return m
}
