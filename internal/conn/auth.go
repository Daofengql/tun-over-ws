package conn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DeviceCredentials stores device authentication credentials.
type DeviceCredentials struct {
	DeviceID    string     `json:"device_id"`
	DeviceInfo  DeviceInfo `json:"device_info"`
	AccessKey   string     `json:"ak,omitempty"`
	RefreshKey  string     `json:"rk,omitempty"`
	AKExpiresAt time.Time  `json:"ak_expires_at,omitempty"`
	RKExpiresAt time.Time  `json:"rk_expires_at,omitempty"`
}

// DeviceAuth handles device authorization flow.
type DeviceAuth struct {
	serverURL string
	apiBase   string
	deviceDir string
	creds     *DeviceCredentials
	info      DeviceInfo
	http      *http.Client
}

// NewDeviceAuth creates a new device auth handler.
func NewDeviceAuth(serverURL, deviceDir string) *DeviceAuth {
	return &DeviceAuth{
		serverURL: serverURL,
		apiBase:   deriveAPIBase(serverURL),
		deviceDir: deviceDir,
		info:      CollectDeviceInfo(),
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// LoadOrCreateCredentials loads existing credentials or prepares for new device registration.
func (d *DeviceAuth) LoadOrCreateCredentials() error {
	deviceDir, err := expandHome(d.deviceDir)
	if err != nil {
		return err
	}
	d.deviceDir = deviceDir
	credPath := filepath.Join(d.deviceDir, "device.json")

	data, err := os.ReadFile(credPath)
	if err == nil {
		var creds DeviceCredentials
		if err := json.Unmarshal(data, &creds); err == nil && creds.DeviceID != "" {
			d.creds = &creds
			return nil
		}
	}

	d.creds = &DeviceCredentials{
		DeviceID:   d.info.StableDeviceID(),
		DeviceInfo: d.info,
	}

	return d.saveCredentials()
}

// saveCredentials saves credentials to disk.
func (d *DeviceAuth) saveCredentials() error {
	if err := os.MkdirAll(d.deviceDir, 0700); err != nil {
		return fmt.Errorf("create device dir: %w", err)
	}

	data, err := json.MarshalIndent(d.creds, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(d.deviceDir, "device.json"), data, 0600)
}

// HasValidKey checks if we have a valid access key.
func (d *DeviceAuth) HasValidKey() bool {
	if d.creds == nil || d.creds.AccessKey == "" {
		return false
	}
	return time.Now().Before(d.creds.AKExpiresAt)
}

// GetAccessKey returns the current access key.
func (d *DeviceAuth) GetAccessKey() string {
	if d.creds == nil {
		return ""
	}
	return d.creds.AccessKey
}

// GetDeviceID returns the stable device identifier.
func (d *DeviceAuth) GetDeviceID() string {
	if d.creds == nil {
		return ""
	}
	return d.creds.DeviceID
}

// RefreshKey attempts to refresh the access key using the refresh key.
func (d *DeviceAuth) RefreshKey() error {
	if d.creds == nil || d.creds.RefreshKey == "" {
		return fmt.Errorf("no refresh key available")
	}

	reqBody := map[string]string{
		"refresh_key": d.creds.RefreshKey,
	}

	body, _ := json.Marshal(reqBody)
	resp, err := d.http.Post(d.apiBase+"/api/auth/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh failed: %d", resp.StatusCode)
	}

	var result struct {
		AccessKey  string `json:"access_key"`
		RefreshKey string `json:"refresh_key"`
		ExpiresIn  int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode refresh response: %w", err)
	}

	d.creds.AccessKey = result.AccessKey
	d.creds.RefreshKey = result.RefreshKey
	d.creds.AKExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	d.creds.RKExpiresAt = time.Now().Add(90 * 24 * time.Hour)

	return d.saveCredentials()
}

// InitDeviceAuth initiates the device authorization flow.
// Returns the auth URL for the user to visit.
func (d *DeviceAuth) InitDeviceAuth() (string, string, error) {
	reqBody := map[string]string{
		"device_id":   d.creds.DeviceID,
		"device_info": d.creds.DeviceInfo.ToJSON(),
	}

	body, _ := json.Marshal(reqBody)
	resp, err := d.http.Post(d.apiBase+"/api/auth/init", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("init auth request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("init auth failed: %d", resp.StatusCode)
	}

	var result struct {
		SessionCode string `json:"session_code"`
		AuthURL     string `json:"auth_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode init response: %w", err)
	}

	return result.AuthURL, result.SessionCode, nil
}

// PollAuthStatus polls for authorization status.
func (d *DeviceAuth) PollAuthStatus(sessionCode string) (string, error) {
	reqBody := map[string]string{
		"session_code": sessionCode,
	}

	body, _ := json.Marshal(reqBody)
	resp, err := d.http.Post(d.apiBase+"/api/auth/poll", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("poll request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("poll failed: %d", resp.StatusCode)
	}

	var result struct {
		Status     string `json:"status"`
		AccessKey  string `json:"access_key"`
		RefreshKey string `json:"refresh_key"`
		VirtualIP  string `json:"virtual_ip"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode poll response: %w", err)
	}

	if result.Status == "approved" {
		d.creds.AccessKey = result.AccessKey
		d.creds.RefreshKey = result.RefreshKey
		d.creds.AKExpiresAt = time.Now().Add(24 * time.Hour)
		d.creds.RKExpiresAt = time.Now().Add(90 * 24 * time.Hour)
		if err := d.saveCredentials(); err != nil {
			return "", fmt.Errorf("save credentials: %w", err)
		}
	}

	return result.Status, nil
}

func deriveAPIBase(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(serverURL, "/")
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	u.Path = strings.TrimSuffix(u.Path, "/tunnel")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func expandHome(path string) (string, error) {
	if path == "" || path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "" || path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}
