package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Daofengql/tun-over-ws/internal/config"
	"github.com/Daofengql/tun-over-ws/internal/store"
	"github.com/rs/zerolog"
)

func newTestHandler(t *testing.T) (*Handler, store.Store) {
	t.Helper()
	s, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := config.DefaultServerConfig()
	cfg.OverlayCIDR = "10.66.0.0/29"
	cfg.ServerTUN.IP = "10.66.0.1"
	cfg.Admin.Username = "admin"
	cfg.Admin.Password = "secret"

	return NewHandler(s, cfg, "test-secret", zerolog.Nop()), s
}

func TestAuthInitAllocatesDistinctVIPs(t *testing.T) {
	h, s := newTestHandler(t)

	for _, deviceID := range []string{"device-a", "device-b"} {
		body := bytes.NewBufferString(`{"device_id":"` + deviceID + `","device_info":"{\"os\":\"test\"}"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/init", body)
		rec := httptest.NewRecorder()

		h.handleAuthInit(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("init %s status=%d body=%s", deviceID, rec.Code, rec.Body.String())
		}
	}

	a, err := s.GetDeviceByID(context.Background(), "device-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.GetDeviceByID(context.Background(), "device-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.AutoVIP != "10.66.0.2" || b.AutoVIP != "10.66.0.3" {
		t.Fatalf("unexpected vips: a=%s b=%s", a.AutoVIP, b.AutoVIP)
	}
}

func TestAuthRefreshRotatesKeys(t *testing.T) {
	h, s := newTestHandler(t)
	ctx := context.Background()
	if err := s.CreateDevice(ctx, &store.Device{
		DeviceID:   "device-a",
		DeviceInfo: `{"os":"test"}`,
		Status:     store.DeviceStatusApproved,
		AutoVIP:    "10.66.0.2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDeviceKeys(ctx, "device-a", hashToken("old-ak"), hashToken("old-rk"), time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", bytes.NewBufferString(`{"refresh_key":"old-rk"}`))
	rec := httptest.NewRecorder()
	h.handleAuthRefresh(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}

	var res struct {
		AccessKey  string `json:"access_key"`
		RefreshKey string `json:"refresh_key"`
		ExpiresIn  int    `json:"expires_in"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.AccessKey == "" || res.RefreshKey == "" || res.ExpiresIn <= 0 {
		t.Fatalf("incomplete refresh response: %+v", res)
	}
	if res.AccessKey == "old-ak" || res.RefreshKey == "old-rk" {
		t.Fatalf("keys were not rotated")
	}
	device, err := s.GetDeviceByAK(ctx, hashToken(res.AccessKey))
	if err != nil {
		t.Fatal(err)
	}
	if device == nil || device.DeviceID != "device-a" {
		t.Fatalf("new access key was not persisted")
	}
}

func TestAuthSessionRequiresAdminJWT(t *testing.T) {
	h, s := newTestHandler(t)
	ctx := context.Background()
	if err := s.CreateDevice(ctx, &store.Device{
		DeviceID:   "device-a",
		DeviceInfo: `{"os":"test"}`,
		Status:     store.DeviceStatusPending,
		AutoVIP:    "10.66.0.2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthSession(ctx, &store.AuthSession{
		SessionCode: "session-a",
		DeviceID:    "device-a",
		Status:      "pending",
		ExpiresAt:   time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/session/session-a", nil)
	rec := httptest.NewRecorder()
	h.requireAuth(http.HandlerFunc(h.handleAuthSession)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", rec.Code)
	}
}

func TestAuthPollIssuesKeysAfterDeviceApproved(t *testing.T) {
	h, s := newTestHandler(t)
	ctx := context.Background()
	if err := s.CreateDevice(ctx, &store.Device{
		DeviceID:   "device-a",
		DeviceInfo: `{"os":"test"}`,
		Status:     store.DeviceStatusPending,
		AutoVIP:    "10.66.0.2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthSession(ctx, &store.AuthSession{
		SessionCode: "session-a",
		DeviceID:    "device-a",
		Status:      "pending",
		ExpiresAt:   time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	device, err := s.GetDeviceByID(ctx, "device-a")
	if err != nil {
		t.Fatal(err)
	}
	device.Status = store.DeviceStatusApproved
	if err := s.UpdateDevice(ctx, "device-a", device); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/poll", bytes.NewBufferString(`{"session_code":"session-a"}`))
	rec := httptest.NewRecorder()
	h.handleAuthPoll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", rec.Code, rec.Body.String())
	}

	var res struct {
		Status    string `json:"status"`
		AccessKey string `json:"access_key"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "approved" || res.AccessKey == "" {
		t.Fatalf("expected approved response with access key, got %+v", res)
	}
}

func TestDeviceApproveAndRevokeEndpoints(t *testing.T) {
	h, s := newTestHandler(t)
	ctx := context.Background()
	if err := s.CreateDevice(ctx, &store.Device{
		DeviceID:   "device-a",
		DeviceInfo: `{"os":"test"}`,
		Status:     store.DeviceStatusPending,
		AutoVIP:    "10.66.0.2",
	}); err != nil {
		t.Fatal(err)
	}
	token, err := h.jwt.GenerateToken(7, "admin")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/web/devices/device-a/approve", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.requireAuth(http.HandlerFunc(h.handleDevice)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	device, err := s.GetDeviceByID(ctx, "device-a")
	if err != nil {
		t.Fatal(err)
	}
	if device.Status != store.DeviceStatusApproved || device.ApprovedBy == nil || *device.ApprovedBy != 7 {
		t.Fatalf("approve did not persist status/admin: %+v", device)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/web/devices/device-a/revoke", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.requireAuth(http.HandlerFunc(h.handleDevice)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	device, err = s.GetDeviceByID(ctx, "device-a")
	if err != nil {
		t.Fatal(err)
	}
	if device.Status != store.DeviceStatusRevoked {
		t.Fatalf("revoke did not persist status: %+v", device)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h, s := newTestHandler(t)
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAdmin(context.Background(), &store.Admin{Username: "admin", Password: hash}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/web/login", bytes.NewBufferString(`{"username":"admin","password":"bad"}`))
		req.RemoteAddr = "192.0.2.10:12345"
		rec := httptest.NewRecorder()
		h.handleLogin(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/web/login", bytes.NewBufferString(`{"username":"admin","password":"bad"}`))
	req.RemoteAddr = "192.0.2.10:12345"
	rec := httptest.NewRecorder()
	h.handleLogin(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected rate limit, got %d body=%s", rec.Code, rec.Body.String())
	}
}
