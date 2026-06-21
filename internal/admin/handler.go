package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Daofengql/tun-over-ws/internal/config"
	"github.com/Daofengql/tun-over-ws/internal/store"
	"github.com/rs/zerolog"
)

// Handler handles admin HTTP API requests.
type Handler struct {
	store        store.Store
	jwt          *JWTManager
	log          zerolog.Logger
	cfg          *config.ServerConfig
	loginLimiter *loginLimiter
}

// NewHandler creates a new admin handler.
func NewHandler(s store.Store, cfg *config.ServerConfig, jwtSecret string, log zerolog.Logger) *Handler {
	return &Handler{
		store:        s,
		jwt:          NewJWTManager(jwtSecret, 24*time.Hour),
		log:          log,
		cfg:          cfg,
		loginLimiter: newLoginLimiter(5, time.Minute),
	}
}

// RegisterRoutes registers admin API routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public routes (no auth required)
	mux.Handle("/api/auth/init", h.withDevCORS(http.HandlerFunc(h.handleAuthInit)))
	mux.Handle("/api/auth/poll", h.withDevCORS(http.HandlerFunc(h.handleAuthPoll)))
	mux.Handle("/api/auth/refresh", h.withDevCORS(http.HandlerFunc(h.handleAuthRefresh)))
	mux.Handle("/api/web/login", h.withDevCORS(http.HandlerFunc(h.handleLogin)))

	// Protected routes (JWT required)
	mux.Handle("/api/auth/session/", h.withDevCORS(h.requireAuth(http.HandlerFunc(h.handleAuthSession))))
	mux.Handle("/api/web/devices", h.withDevCORS(h.requireAuth(http.HandlerFunc(h.handleDevices))))
	mux.Handle("/api/web/devices/", h.withDevCORS(h.requireAuth(http.HandlerFunc(h.handleDevice))))

	h.registerStaticRoutes(mux)
}

func (h *Handler) registerStaticRoutes(mux *http.ServeMux) {
	staticDir := strings.TrimSpace(h.cfg.Admin.StaticDir)
	if staticDir != "" {
		root := http.Dir(staticDir)
		mux.Handle("/assets/", http.FileServer(root))
		mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
			data, err := os.ReadFile(filepath.Join(staticDir, "index.html"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
		})
		h.registerRootRedirect(mux)
		return
	}

	staticRoot, err := fs.Sub(staticContent, "static")
	if err != nil {
		panic(err)
	}

	mux.Handle("/assets/", http.FileServer(http.FS(staticRoot)))
	mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(staticRoot, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	h.registerRootRedirect(mux)
}

func (h *Handler) registerRootRedirect(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}

func (h *Handler) withDevCORS(next http.Handler) http.Handler {
	origin := strings.TrimSpace(h.cfg.Admin.DevOrigin)
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth is middleware that validates JWT token.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ExtractBearerToken(r)
		if token == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}

		claims, err := h.jwt.ValidateToken(token)
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}

		// Store claims in context for handlers
		r = r.WithContext(withClaims(r.Context(), claims))
		next.ServeHTTP(w, r)
	})
}

// handleLogin handles admin login.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	remote := clientAddr(r)
	if !h.loginLimiter.Allow(remote) {
		http.Error(w, `{"error":"too many login attempts"}`, http.StatusTooManyRequests)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	admin, err := h.store.GetAdminByUsername(r.Context(), req.Username)
	if err != nil || admin == nil {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	if !checkPassword(req.Password, admin.Password) {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	h.loginLimiter.Reset(remote)

	token, err := h.jwt.GenerateToken(admin.ID, admin.Username)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to generate token")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	h.store.UpdateAdminLastLogin(r.Context(), admin.ID)

	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

type loginLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	attempts map[string][]time.Time
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		limit:    limit,
		window:   window,
		attempts: make(map[string][]time.Time),
	}
}

func (l *loginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)
	attempts := l.attempts[key]
	kept := attempts[:0]
	for _, at := range attempts {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.limit {
		l.attempts[key] = kept
		return false
	}
	l.attempts[key] = append(kept, now)
	return true
}

func (l *loginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

func clientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return strings.TrimSpace(xrip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// handleDevices handles device listing.
func (h *Handler) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	devices, err := h.store.ListDevices(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("failed to list devices")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"devices": devices})
}

// handleDevice handles single device operations.
func (h *Handler) handleDevice(w http.ResponseWriter, r *http.Request) {
	// Extract device ID from path: /api/web/devices/{device_id}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		return
	}
	deviceID := parts[4]
	action := ""
	if len(parts) >= 6 {
		action = parts[5]
	}

	if action != "" {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		switch action {
		case "approve":
			h.handleSetDeviceStatus(w, r, deviceID, store.DeviceStatusApproved)
		case "revoke":
			h.handleSetDeviceStatus(w, r, deviceID, store.DeviceStatusRevoked)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGetDevice(w, r, deviceID)
	case http.MethodPut:
		h.handleUpdateDevice(w, r, deviceID)
	case http.MethodDelete:
		h.handleDeleteDevice(w, r, deviceID)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleSetDeviceStatus(w http.ResponseWriter, r *http.Request, deviceID string, status store.DeviceStatus) {
	device, err := h.store.GetDeviceByID(r.Context(), deviceID)
	if err != nil {
		h.log.Error().Err(err).Str("device_id", deviceID).Msg("failed to get device")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	if device == nil {
		http.Error(w, `{"error":"device not found"}`, http.StatusNotFound)
		return
	}

	device.Status = status
	if claims := GetClaims(r.Context()); claims != nil && status == store.DeviceStatusApproved {
		device.ApprovedBy = &claims.AdminID
	}
	if err := h.store.UpdateDevice(r.Context(), deviceID, device); err != nil {
		h.log.Error().Err(err).Str("device_id", deviceID).Msg("failed to update device status")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(device)
}

func (h *Handler) handleGetDevice(w http.ResponseWriter, r *http.Request, deviceID string) {
	device, err := h.store.GetDeviceByID(r.Context(), deviceID)
	if err != nil {
		h.log.Error().Err(err).Str("device_id", deviceID).Msg("failed to get device")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	if device == nil {
		http.Error(w, `{"error":"device not found"}`, http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(device)
}

func (h *Handler) handleUpdateDevice(w http.ResponseWriter, r *http.Request, deviceID string) {
	var req struct {
		Name      string  `json:"name"`
		VirtualIP *string `json:"virtual_ip"`
		Status    string  `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	device, err := h.store.GetDeviceByID(r.Context(), deviceID)
	if err != nil {
		h.log.Error().Err(err).Str("device_id", deviceID).Msg("failed to get device")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	if device == nil {
		http.Error(w, `{"error":"device not found"}`, http.StatusNotFound)
		return
	}

	if req.Name != "" {
		device.Name = req.Name
	}
	if req.VirtualIP != nil {
		if *req.VirtualIP != "" {
			if err := h.validateDeviceVIP(r.Context(), deviceID, *req.VirtualIP); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
				return
			}
		}
		device.VirtualIP = req.VirtualIP
	}
	if req.Status != "" {
		if !validDeviceStatus(req.Status) {
			http.Error(w, `{"error":"invalid status"}`, http.StatusBadRequest)
			return
		}
		device.Status = store.DeviceStatus(req.Status)
	}

	if err := h.store.UpdateDevice(r.Context(), deviceID, device); err != nil {
		h.log.Error().Err(err).Str("device_id", deviceID).Msg("failed to update device")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(device)
}

func (h *Handler) handleDeleteDevice(w http.ResponseWriter, r *http.Request, deviceID string) {
	if err := h.store.DeleteDevice(r.Context(), deviceID); err != nil {
		h.log.Error().Err(err).Str("device_id", deviceID).Msg("failed to delete device")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// handleAuthInit handles device authorization initiation.
func (h *Handler) handleAuthInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		DeviceID   string `json:"device_id"`
		DeviceInfo string `json:"device_info"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.DeviceID == "" {
		http.Error(w, `{"error":"device_id is required"}`, http.StatusBadRequest)
		return
	}

	// Get or create device
	device, err := h.store.GetDeviceByID(r.Context(), req.DeviceID)
	if err != nil {
		h.log.Error().Err(err).Str("device_id", req.DeviceID).Msg("failed to get device")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	if device == nil {
		vip, err := h.allocateVIP(r.Context())
		if err != nil {
			h.log.Error().Err(err).Str("device_id", req.DeviceID).Msg("failed to allocate vip")
			http.Error(w, `{"error":"failed to allocate virtual ip"}`, http.StatusInternalServerError)
			return
		}

		// Create new device with auto VIP
		device = &store.Device{
			DeviceID:   req.DeviceID,
			DeviceInfo: req.DeviceInfo,
			Status:     store.DeviceStatusPending,
			AutoVIP:    vip.String(),
		}
		if err := h.store.CreateDevice(r.Context(), device); err != nil {
			h.log.Error().Err(err).Str("device_id", req.DeviceID).Msg("failed to create device")
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
	}

	// Create auth session
	code, err := GenerateRandomToken(16)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to generate session code")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	session := &store.AuthSession{
		SessionCode: code,
		DeviceID:    req.DeviceID,
		Status:      "pending",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	}
	if err := h.store.CreateAuthSession(r.Context(), session); err != nil {
		h.log.Error().Err(err).Msg("failed to create auth session")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	// Build auth URL
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	authURL := scheme + "://" + r.Host + "/admin/device-auth?code=" + code

	json.NewEncoder(w).Encode(map[string]string{
		"session_code": code,
		"auth_url":     authURL,
	})
}

// handleAuthRefresh renews device access and refresh keys.
func (h *Handler) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		RefreshKey string `json:"refresh_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.RefreshKey == "" {
		http.Error(w, `{"error":"refresh_key is required"}`, http.StatusBadRequest)
		return
	}

	device, err := h.store.GetDeviceByRK(r.Context(), hashToken(req.RefreshKey))
	if err != nil {
		h.log.Error().Err(err).Msg("failed to lookup refresh key")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	if device == nil || device.Status != store.DeviceStatusApproved {
		http.Error(w, `{"error":"invalid refresh key"}`, http.StatusUnauthorized)
		return
	}
	if device.RKExpiresAt == nil || time.Now().After(*device.RKExpiresAt) {
		http.Error(w, `{"error":"refresh key expired"}`, http.StatusUnauthorized)
		return
	}

	ak, err := GenerateRandomToken(32)
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	rk, err := GenerateRandomToken(32)
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	akExpiry := time.Now().Add(24 * time.Hour)
	rkExpiry := time.Now().Add(90 * 24 * time.Hour)
	if err := h.store.UpdateDeviceKeys(r.Context(), device.DeviceID, hashToken(ak), hashToken(rk), akExpiry, rkExpiry); err != nil {
		h.log.Error().Err(err).Str("device_id", device.DeviceID).Msg("failed to update refreshed keys")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_key":  ak,
		"refresh_key": rk,
		"expires_in":  int((24 * time.Hour).Seconds()),
		"virtual_ip":  device.EffectiveIP(),
	})
}

// handleAuthPoll handles device authorization polling.
func (h *Handler) handleAuthPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionCode string `json:"session_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	session, err := h.store.GetAuthSession(r.Context(), req.SessionCode)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get auth session")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	if session == nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	if time.Now().After(session.ExpiresAt) {
		json.NewEncoder(w).Encode(map[string]string{"status": "expired"})
		return
	}

	device, err := h.store.GetDeviceByID(r.Context(), session.DeviceID)
	if err != nil || device == nil {
		http.Error(w, `{"error":"device not found"}`, http.StatusInternalServerError)
		return
	}

	if session.Status != "approved" {
		if device.Status != store.DeviceStatusApproved {
			json.NewEncoder(w).Encode(map[string]string{"status": session.Status})
			return
		}
		if err := h.store.UpdateAuthSessionStatus(r.Context(), req.SessionCode, "approved"); err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
	}

	// Session approved - generate AK/RK
	ak, err := GenerateRandomToken(32)
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	rk, err := GenerateRandomToken(32)
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	// Store hashed keys
	if err := h.store.UpdateDeviceKeys(r.Context(), device.DeviceID, hashToken(ak), hashToken(rk),
		time.Now().Add(24*time.Hour), time.Now().Add(90*24*time.Hour)); err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"status":      "approved",
		"access_key":  ak,
		"refresh_key": rk,
		"virtual_ip":  device.EffectiveIP(),
	})
}

// handleAuthSession handles auth session viewing (for admin approval page).
func (h *Handler) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	// Extract code from path: /api/auth/session/{code}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		return
	}
	code := parts[4]

	session, err := h.store.GetAuthSession(r.Context(), code)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to get auth session")
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	if session == nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	device, err := h.store.GetDeviceByID(r.Context(), session.DeviceID)
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	// For POST requests, approve the session
	if r.Method == http.MethodPost {
		if err := h.store.UpdateAuthSessionStatus(r.Context(), code, "approved"); err != nil {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "approved"})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"session": session,
		"device":  device,
	})
}

func (h *Handler) allocateVIP(ctx context.Context) (netip.Addr, error) {
	prefix, err := netip.ParsePrefix(h.cfg.OverlayCIDR)
	if err != nil {
		return netip.Addr{}, err
	}
	serverIP, err := netip.ParseAddr(h.cfg.ServerTUN.IP)
	if err != nil {
		return netip.Addr{}, err
	}
	allocated, err := h.store.GetAllocatedVIPs(ctx)
	if err != nil {
		return netip.Addr{}, err
	}

	base, end, err := ipv4Range(prefix)
	if err != nil {
		return netip.Addr{}, err
	}
	for n := base; n <= end; n++ {
		ip := uint32ToIPv4(n)
		if ip == serverIP {
			continue
		}
		if _, exists := allocated[ip.String()]; exists {
			continue
		}
		return ip, nil
	}
	return netip.Addr{}, fmt.Errorf("no available virtual ip in %s", prefix)
}

func (h *Handler) validateDeviceVIP(ctx context.Context, deviceID, vip string) error {
	ip, err := netip.ParseAddr(vip)
	if err != nil || !ip.Is4() {
		return fmt.Errorf("virtual_ip must be an IPv4 address")
	}
	prefix, err := netip.ParsePrefix(h.cfg.OverlayCIDR)
	if err != nil {
		return err
	}
	if !prefix.Contains(ip) {
		return fmt.Errorf("virtual_ip must be inside %s", h.cfg.OverlayCIDR)
	}
	if ip.String() == h.cfg.ServerTUN.IP {
		return fmt.Errorf("virtual_ip cannot equal server_tun.ip")
	}
	existing, err := h.store.GetDeviceByVIP(ctx, vip)
	if err != nil {
		return err
	}
	if existing != nil && existing.DeviceID != deviceID {
		return fmt.Errorf("virtual_ip is already allocated")
	}
	return nil
}

func validDeviceStatus(status string) bool {
	switch store.DeviceStatus(status) {
	case store.DeviceStatusPending, store.DeviceStatusApproved, store.DeviceStatusRevoked:
		return true
	default:
		return false
	}
}

func ipv4Range(prefix netip.Prefix) (uint32, uint32, error) {
	if !prefix.Addr().Is4() {
		return 0, 0, fmt.Errorf("overlay prefix must be IPv4")
	}
	bits := prefix.Bits()
	if bits < 0 || bits > 32 {
		return 0, 0, fmt.Errorf("invalid IPv4 prefix bits")
	}
	start := ipv4ToUint32(prefix.Masked().Addr())
	size := uint64(1) << uint(32-bits)
	first := uint64(start)
	last := first + size - 1
	if bits <= 30 {
		first++
		last--
	}
	if last > math.MaxUint32 || first > last {
		return 0, 0, fmt.Errorf("empty IPv4 prefix")
	}
	return uint32(first), uint32(last), nil
}

func ipv4ToUint32(ip netip.Addr) uint32 {
	b := ip.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func uint32ToIPv4(n uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}
