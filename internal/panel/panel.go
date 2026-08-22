package panel

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/silenceremember/sbp-panel/internal/apiresponse"
	"github.com/silenceremember/sbp-panel/internal/buildinfo"
	"github.com/silenceremember/sbp-panel/internal/config"
	"github.com/silenceremember/sbp-panel/internal/store"
	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web/*
var embedded embed.FS

type attempt struct {
	Count int
	Since time.Time
}

const (
	attemptWindow               = 15 * time.Minute
	maxTrackedIPEntries         = 4096
	maxConcurrentPasswordChecks = 2
)

var managedMethodByComponent = map[string]string{
	"xray": "xray", "xray-xhttp": "xray-xhttp", "amneziawg": "amneziawg", "bypass-wb": "bypass-wb",
	"bypass-telemost": "bypass-telemost", "bypass-dion": "bypass-dion", "bypass-vk": "bypass-vk",
}

type server struct {
	cfg           config.Config
	db            *store.Store
	agent         *http.Client
	mu            sync.Mutex
	tries         map[string]attempt
	checks        map[string]attempt
	passwordSlots chan struct{}
	credentialMu  sync.Mutex
	metricsMu     sync.Mutex
	metricsSaved  time.Time
	lastSweep     time.Time
}
type ctxKey string

const accountKey ctxKey = "account"
const csrfKey ctxKey = "csrf"

func Run(configPath string) error {
	c, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Database), 0750); err != nil {
		return err
	}
	db, err := store.Open(c.Database)
	if err != nil {
		return err
	}
	defer db.DB.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", c.AgentSocket)
	}}
	s := &server{cfg: c, db: db, agent: &http.Client{Transport: transport, Timeout: 5 * time.Minute}, tries: map[string]attempt{}, checks: map[string]attempt{}}
	go s.accessLoop()
	mux := http.NewServeMux()
	s.routes(mux)
	h := securityHeaders(mux)
	httpServer := &http.Server{Addr: c.Listen, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	fmt.Printf("Simple Bridge Panel listening on https://0.0.0.0%s\n", c.Listen)
	return httpServer.ListenAndServeTLS(c.TLSCert, c.TLSKey)
}

func (s *server) routes(m *http.ServeMux) {
	webFS, _ := fs.Sub(embedded, "web")
	auth := func(handler http.HandlerFunc) http.Handler { return s.auth(handler) }
	admin := func(handler http.HandlerFunc) http.Handler { return s.auth(s.admin(handler)) }
	m.Handle("GET /", http.FileServer(http.FS(webFS)))
	m.HandleFunc("GET /check", s.checkPage)
	m.HandleFunc("GET /check/{name...}", s.checkPage)
	m.HandleFunc("POST /api/check-group", s.checkGroup)
	m.HandleFunc("GET /api/bootstrap/status", s.bootstrapStatus)
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/password", s.changePassword)
	m.Handle("POST /api/logout", auth(s.logout))
	m.Handle("GET /api/state", auth(s.state))
	m.Handle("PUT /api/settings/server-url", admin(s.updateServerURL))
	m.Handle("POST /api/groups", admin(s.createGroup))
	m.Handle("PUT /api/groups/{id}", admin(s.updateGroup))
	m.Handle("DELETE /api/groups/{id}", admin(s.deleteGroup))
	m.Handle("POST /api/groups/{id}/extend", admin(s.extendGroup))
	m.Handle("GET /api/groups/{id}/devices", auth(s.devices))
	m.Handle("POST /api/groups/{id}/devices", admin(s.createDevice))
	m.Handle("PUT /api/devices/{id}/enabled", admin(s.toggleDevice))
	m.Handle("PUT /api/devices/{id}", admin(s.updateDevice))
	m.Handle("DELETE /api/devices/{id}", admin(s.deleteDevice))
	m.Handle("GET /api/devices/{id}/credential", auth(s.deviceCredential))
	m.Handle("GET /api/devices/{id}/qr", auth(s.deviceQR))
	m.Handle("GET /api/discovery", auth(s.discovery))
	m.Handle("GET /api/metrics", auth(s.metrics))
	m.Handle("GET /api/bypass/rooms", auth(s.bypassRooms))
	m.Handle("GET /api/update", auth(s.updateInfo))
	m.Handle("POST /api/update", admin(s.applyUpdate))
	m.Handle("GET /api/update/progress", auth(s.updateProgress))
	m.Handle("POST /api/components/{id}/install", admin(s.installComponent))
	m.Handle("DELETE /api/components/{id}", admin(s.uninstallComponent))
	m.Handle("GET /api/components/{id}/install", auth(s.installStatus))
	m.Handle("POST /api/bypass/{provider}/credentials", admin(s.uploadBypass))
	m.Handle("DELETE /api/bypass/{provider}/credentials", admin(s.clearBypass))
}

func (s *server) checkPage(w http.ResponseWriter, _ *http.Request) {
	page, err := embedded.ReadFile("web/check.html")
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}

func (s *server) checkGroup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowCheckAttempt(r) {
		fail(w, http.StatusTooManyRequests, errors.New("too many checks; wait 15 minutes"))
		return
	}
	var in struct{ Name string }
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, errors.New("enter a group name"))
		return
	}
	normalizedName, err := store.NormalizeGroupName(in.Name)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	in.Name = normalizedName
	group, err := s.db.FindGroupByName(in.Name)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, errors.New("group not found; check the name and try again"))
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, errors.New("could not check the group"))
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "group": map[string]any{
		"name": group.Name, "status": group.Status, "expires_at": group.ExpiresAt, "unlimited": group.Unlimited,
	}})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; img-src 'self' data:; connect-src 'self'")
		if r.URL.Path == "/check" || strings.HasPrefix(r.URL.Path, "/check/") || r.URL.Path == "/api/check-group" {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		next.ServeHTTP(w, r)
	})
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	apiresponse.JSON(w, status, v)
}
func fail(w http.ResponseWriter, status int, err error) {
	apiresponse.WriteError(w, status, err)
}
func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}
func numParam(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(r.PathValue(key), 10, 64)
}

func (s *server) allowAttempt(r *http.Request) bool {
	return s.recordAttempt(clientIP(r), false, 10)
}

func (s *server) clearAttempts(r *http.Request) {
	s.mu.Lock()
	delete(s.tries, clientIP(r))
	s.mu.Unlock()
}

func (s *server) allowCheckAttempt(r *http.Request) bool {
	return s.recordAttempt(clientIP(r), true, 30)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) recordAttempt(host string, publicCheck bool, limit int) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tries == nil {
		s.tries = map[string]attempt{}
	}
	if s.checks == nil {
		s.checks = map[string]attempt{}
	}
	if s.lastSweep.IsZero() || now.Sub(s.lastSweep) >= time.Minute {
		s.pruneAttemptsLocked(now)
		s.lastSweep = now
	}
	entries := s.tries
	if publicCheck {
		entries = s.checks
	}
	if _, exists := entries[host]; !exists {
		s.makeAttemptRoomLocked()
	}
	entry := entries[host]
	if entry.Since.IsZero() || now.Sub(entry.Since) > attemptWindow {
		entry = attempt{Since: now}
	}
	entry.Count++
	entries[host] = entry
	return entry.Count <= limit
}

func (s *server) pruneAttemptsLocked(now time.Time) {
	for key, value := range s.tries {
		if now.Sub(value.Since) > attemptWindow {
			delete(s.tries, key)
		}
	}
	for key, value := range s.checks {
		if now.Sub(value.Since) > attemptWindow {
			delete(s.checks, key)
		}
	}
}

func (s *server) makeAttemptRoomLocked() {
	for len(s.tries)+len(s.checks) >= maxTrackedIPEntries {
		removed := false
		if len(s.checks) >= len(s.tries) {
			for key := range s.checks {
				delete(s.checks, key)
				removed = true
				break
			}
		} else {
			for key := range s.tries {
				delete(s.tries, key)
				removed = true
				break
			}
		}
		if !removed {
			break
		}
	}
}

func (s *server) beginPasswordCheck() (func(), bool) {
	s.mu.Lock()
	if s.passwordSlots == nil {
		s.passwordSlots = make(chan struct{}, maxConcurrentPasswordChecks)
	}
	slots := s.passwordSlots
	s.mu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return nil, false
	}
}

func (s *server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	exists, err := s.db.OwnerExists()
	if err != nil {
		fail(w, 500, err)
		return
	}
	jsonOut(w, 200, map[string]any{"needs_bootstrap": !exists})
}
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allowAttempt(r) {
		fail(w, http.StatusTooManyRequests, errors.New("too many attempts; wait 15 minutes"))
		return
	}
	var in struct{ Login, Password string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if in.Login != "admin" {
		fail(w, 401, errors.New("invalid credentials"))
		return
	}
	release, ok := s.beginPasswordCheck()
	if !ok {
		fail(w, http.StatusTooManyRequests, errors.New("password verification is busy; try again shortly"))
		return
	}
	defer release()
	a, err := s.db.Authenticate(in.Login, in.Password)
	if err != nil {
		fail(w, 401, err)
		return
	}
	token, csrf, err := s.db.CreateSession(a.ID)
	if err != nil {
		fail(w, 500, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "vpn_session", Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	s.clearAttempts(r)
	jsonOut(w, 200, map[string]any{"ok": true, "csrf": csrf, "account": a})
}

func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	if !s.allowAttempt(r) {
		fail(w, http.StatusTooManyRequests, errors.New("too many attempts; try again in 15 minutes"))
		return
	}
	var in struct {
		CurrentPassword string
		NewPassword     string
		ConfirmPassword string
	}
	if err := decode(r, &in); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if in.NewPassword != in.ConfirmPassword {
		fail(w, http.StatusBadRequest, errors.New("the new passwords do not match"))
		return
	}
	if len(in.NewPassword) < 8 {
		fail(w, http.StatusBadRequest, errors.New("the new password must contain at least 8 characters"))
		return
	}
	release, ok := s.beginPasswordCheck()
	if !ok {
		fail(w, http.StatusTooManyRequests, errors.New("password verification is busy; try again shortly"))
		return
	}
	defer release()
	if err := s.db.ChangeOwnerPassword(in.CurrentPassword, in.NewPassword); err != nil {
		if err.Error() == "current password is incorrect" {
			fail(w, http.StatusUnauthorized, errors.New("the current password is incorrect"))
			return
		}
		fail(w, http.StatusBadRequest, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "vpn_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	s.clearAttempts(r)
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "message": "password changed; all previous sessions have been signed out"})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("vpn_session"); err == nil {
		s.db.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "vpn_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("vpn_session")
		if err != nil {
			fail(w, 401, errors.New("authentication required"))
			return
		}
		a, csrf, err := s.db.Session(c.Value)
		if err != nil {
			fail(w, 401, errors.New("session expired"))
			return
		}
		if r.Method != "GET" && r.Header.Get("X-CSRF-Token") != csrf {
			fail(w, 403, errors.New("invalid CSRF token"))
			return
		}
		ctx := context.WithValue(r.Context(), accountKey, a)
		ctx = context.WithValue(ctx, csrfKey, csrf)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (s *server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !owner(r) {
			fail(w, http.StatusForbidden, errors.New("admin required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func owner(r *http.Request) bool {
	a, _ := r.Context().Value(accountKey).(store.Account)
	return a.Role == "owner" || a.Role == "admin"
}
func (s *server) state(w http.ResponseWriter, r *http.Request) {
	groups, err := s.db.ListGroups()
	if err != nil {
		fail(w, 500, err)
		return
	}
	devices, err := s.db.ListAllDeviceMetadata()
	if err != nil {
		fail(w, 500, err)
		return
	}
	serverURL, err := s.db.Setting("server_url")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "account": r.Context().Value(accountKey), "csrf": r.Context().Value(csrfKey), "groups": groups, "devices": devices, "server_url": serverURL, "version": buildinfo.Version, "repository": buildinfo.RepositoryURL()})
}

func groupAccessEnabled(group store.Group) bool {
	return group.Status != "expired"
}

func (s *server) accessLoop() {
	_ = s.reconcileAllAccess()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		_ = s.reconcileAllAccess()
	}
}

func (s *server) reconcileAllAccess() error {
	groups, err := s.db.ListGroups()
	if err != nil {
		return err
	}
	var failures []error
	for _, group := range groups {
		if group.AccessApplied == groupAccessEnabled(group) {
			continue
		}
		if err := s.reconcileGroupAccess(group.ID); err != nil {
			failures = append(failures, fmt.Errorf("group %d: %w", group.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *server) reconcileGroupAccess(groupID int64) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	return s.reconcileGroupAccessLocked(groupID)
}

func (s *server) reconcileGroupAccessLocked(groupID int64) error {
	group, err := s.db.Group(groupID)
	if err != nil {
		return err
	}
	desired := groupAccessEnabled(group)
	if group.AccessApplied == desired {
		return nil
	}
	devices, err := s.db.ListDevices(groupID)
	if err != nil {
		return err
	}
	if desired {
		for _, device := range devices {
			if device.Enabled && !isBypassMethod(device.Method) {
				if err := s.controlCredential(device, true); err != nil {
					return fmt.Errorf("failed to restore %q: %w", device.Name, err)
				}
			}
		}
		for _, method := range uniqueBypassMethods(devices) {
			if _, err := s.provisionCredential(groupID, group.Name, method); err != nil {
				return fmt.Errorf("failed to restore the %s room: %w", strings.TrimPrefix(method, "bypass-"), err)
			}
		}
	} else {
		for _, device := range devices {
			if device.Enabled && !isBypassMethod(device.Method) {
				if err := s.controlCredential(device, false); err != nil {
					return fmt.Errorf("failed to suspend %q: %w", device.Name, err)
				}
			}
		}
		for _, method := range uniqueBypassMethods(devices) {
			if err := s.removeBypassRoom(groupID, method, true); err != nil {
				return fmt.Errorf("failed to suspend the %s room: %w", strings.TrimPrefix(method, "bypass-"), err)
			}
		}
	}
	return s.db.SetGroupAccessApplied(groupID, desired)
}

func (s *server) updateServerURL(w http.ResponseWriter, r *http.Request) {
	var in struct{ URL string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	if len(in.URL) > 2048 {
		fail(w, 400, errors.New("the URL is too long"))
		return
	}
	if in.URL != "" {
		u, err := url.Parse(in.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			fail(w, 400, errors.New("enter a full HTTP(S) URL, for example https://hosting.example"))
			return
		}
	}
	if err := s.db.SetSetting("server_url", in.URL); err != nil {
		fail(w, 500, err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "server_url": in.URL})
}
func (s *server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string
		Contact   string
		Days      int
		Unlimited bool
		ExpiresAt string
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	id, err := s.db.CreateGroupWithExpiration(in.Name, in.Contact, in.Days, in.Unlimited, in.ExpiresAt)
	if err != nil {
		fail(w, 400, err)
		return
	}
	jsonOut(w, 201, map[string]any{"ok": true, "id": id})
}
func (s *server) updateGroup(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	var in struct {
		Name      string
		Contact   string
		ExpiresAt string
		Unlimited bool
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.db.UpdateGroupDetails(id, in.Name, in.Contact, in.ExpiresAt, in.Unlimited); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.reconcileGroupAccess(id); err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("group saved, but access synchronization is pending: %w", err))
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (s *server) extendGroup(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.db.ExtendGroupMonth(id); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.reconcileGroupAccess(id); err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("group extended, but access synchronization is pending: %w", err))
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (s *server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	devices, err := s.db.ListDevices(id)
	if err != nil {
		fail(w, 500, err)
		return
	}
	group, err := s.db.Group(id)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	// Mark the external state as needing reconciliation before touching it. If
	// deletion fails halfway through, the periodic reconciler repairs access.
	if err := s.db.SetGroupAccessApplied(id, !groupAccessEnabled(group)); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	var malformed []string
	xrayDevices := map[string][]store.Device{}
	for _, device := range devices {
		if !device.Enabled || isBypassMethod(device.Method) {
			continue
		}
		if device.Method == "xray" || device.Method == "xray-xhttp" {
			if _, ok := deviceTrafficPublicID(device); !ok {
				malformed = append(malformed, device.Name)
				continue
			}
			xrayDevices[device.Method] = append(xrayDevices[device.Method], device)
			continue
		}
		if err := s.controlCredential(device, false); err != nil {
			if discardableCredentialError(err) {
				malformed = append(malformed, device.Name)
				continue
			}
			fail(w, 400, fmt.Errorf("failed to revoke credential %q: %w", device.Name, err))
			return
		}
	}
	for _, method := range []string{"xray", "xray-xhttp"} {
		devices := xrayDevices[method]
		if len(devices) == 0 {
			continue
		}
		if err := s.controlXrayCredentials(method, devices, false); err != nil {
			fail(w, 400, fmt.Errorf("failed to revoke %d %s credential(s): %w", len(devices), method, err))
			return
		}
	}
	bypassMethods := uniqueBypassMethods(devices)
	for _, method := range bypassMethods {
		if err := s.removeBypassRoom(id, method, true); err != nil {
			fail(w, 400, fmt.Errorf("failed to remove the dedicated bypass room: %w", err))
			return
		}
	}
	if err := s.db.DeleteGroup(id); err != nil {
		fail(w, 400, err)
		return
	}
	result := map[string]any{"ok": true}
	var cleanupWarnings []string
	for _, method := range bypassMethods {
		if err := s.removeBypassRoom(id, method, false); err != nil {
			cleanupWarnings = append(cleanupWarnings, strings.TrimPrefix(method, "bypass-")+": "+err.Error())
		}
	}
	if len(malformed) > 0 {
		cleanupWarnings = append(cleanupWarnings, fmt.Sprintf("%d malformed credential(s) could not be identified for revocation: %s", len(malformed), strings.Join(malformed, ", ")))
	}
	if len(cleanupWarnings) > 0 {
		result["warning"] = "Group removed. Cleanup needs attention: " + strings.Join(cleanupWarnings, "; ")
	}
	jsonOut(w, 200, result)
}
func (s *server) devices(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	v, err := s.db.ListDeviceMetadata(id)
	if err != nil {
		fail(w, 500, err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "devices": v})
}
func (s *server) createDevice(w http.ResponseWriter, r *http.Request) {
	gid, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	var in struct{ Name, Method, Format string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	in.Method, in.Format = normalizeDeviceMethod(in.Method, in.Format)
	if err := s.db.ValidateDevice(gid, in.Name, in.Method, in.Format); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	// Two identical POSTs can both pass validation before the first request
	// finishes provisioning. Recheck under the same lock so only one credential
	// reaches the managed runtime.
	existing, found, err := s.db.MatchingDevice(gid, in.Name, in.Method, in.Format)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if found {
		fail(w, http.StatusConflict, fmt.Errorf("device %q already exists in this group for the selected protocol", existing.Name))
		return
	}
	credential, err := s.provisionCredential(gid, in.Name, in.Method)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.db.CreateDevice(gid, in.Name, in.Method, credential, in.Format)
	if err != nil {
		// A bypass room belongs to the whole group. Never remove an existing
		// shared room when a single device row fails to insert.
		if !isBypassMethod(in.Method) {
			_ = s.controlCredential(store.Device{Name: in.Name, Method: in.Method, Credential: credential, Enabled: true}, false)
		}
		fail(w, http.StatusBadRequest, err)
		return
	}
	device := store.Device{ID: id, GroupID: gid, Name: in.Name, Method: in.Method, Format: in.Format, Credential: credential, Enabled: true}
	group, groupErr := s.db.Group(gid)
	if groupErr == nil && !groupAccessEnabled(group) {
		var suspendErr error
		if isBypassMethod(in.Method) {
			suspendErr = s.removeBypassRoom(gid, in.Method, true)
		} else {
			suspendErr = s.controlCredential(device, false)
		}
		if suspendErr != nil {
			_ = s.db.DeleteDevice(id)
			fail(w, http.StatusBadGateway, fmt.Errorf("the expired group could not keep the new credential suspended: %w", suspendErr))
			return
		}
	}
	jsonOut(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "credential": displayCredential(device)})
}

func (s *server) provisionCredential(groupID int64, name, method string) (string, error) {
	group, err := s.db.Group(groupID)
	if err != nil {
		return "", fmt.Errorf("load credential group: %w", err)
	}
	profileName := fmt.Sprintf("SBP · %s · %s", strings.TrimSpace(group.Name), strings.TrimSpace(name))
	payload, _ := json.Marshal(map[string]any{"name": profileName, "method": method, "group_id": groupID})
	req, _ := http.NewRequest("POST", "http://unix/v1/credentials", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.agent.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	var provisioned struct {
		OK         bool   `json:"ok"`
		Credential string `json:"credential"`
		Error      string `json:"error"`
	}
	if json.NewDecoder(resp.Body).Decode(&provisioned) != nil || resp.StatusCode != http.StatusOK || !provisioned.OK {
		if provisioned.Error == "" {
			provisioned.Error = "failed to create a credential for the selected method"
		}
		return "", errors.New(provisioned.Error)
	}
	return provisioned.Credential, nil
}

func normalizeDeviceMethod(method, format string) (string, string) {
	switch strings.TrimSpace(method) {
	case "amneziawg-app":
		return "amneziawg", "app"
	case "amneziawg-native":
		return "amneziawg", "native"
	default:
		return strings.TrimSpace(method), strings.TrimSpace(format)
	}
}

func (s *server) toggleDevice(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	var in struct{ Enabled bool }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	d, err := s.db.Device(id)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	group, err := s.db.Group(d.GroupID)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	appliedEnabled := in.Enabled && groupAccessEnabled(group)
	if err := s.controlCredential(d, appliedEnabled); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.db.ToggleDevice(id, in.Enabled); err != nil {
		_ = s.controlCredential(d, d.Enabled && groupAccessEnabled(group))
		fail(w, 500, err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}
func (s *server) updateDevice(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	var in struct{ Name string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	current, err := s.db.Device(id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	if err := s.db.UpdateDevice(id, current.GroupID, in.Name); err != nil {
		fail(w, 400, err)
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (s *server) deviceQR(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	d, err := s.db.Device(id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	if d.Credential == "" {
		fail(w, 404, errors.New("this device does not have a credential yet"))
		return
	}
	png, err := qrcode.Encode(displayCredential(d), qrcode.Medium, 320)
	if err != nil {
		fail(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *server) deviceCredential(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	d, err := s.db.Device(id)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	if d.Credential == "" {
		fail(w, http.StatusNotFound, errors.New("this device does not have a credential yet"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "credential": displayCredential(d)})
}

func displayCredential(d store.Device) string {
	if d.Method != "amneziawg" || d.Format != "app" || d.Credential == "" {
		return d.Credential
	}
	var compressed bytes.Buffer
	// qCompress stores the uncompressed length as a 4-byte big-endian prefix,
	// followed by the zlib stream. Amnezia's vpn:// importer uses qUncompress.
	_ = binary.Write(&compressed, binary.BigEndian, uint32(len([]byte(d.Credential))))
	zw, err := zlib.NewWriterLevel(&compressed, 8)
	if err != nil {
		return d.Credential
	}
	_, _ = zw.Write([]byte(d.Credential))
	_ = zw.Close()
	return "vpn://" + base64.RawURLEncoding.EncodeToString(compressed.Bytes())
}
func (s *server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id, err := numParam(r, "id")
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	d, err := s.db.Device(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			jsonOut(w, http.StatusOK, map[string]any{
				"ok":      true,
				"warning": "Device was already removed. The dashboard has been refreshed.",
			})
			return
		}
		fail(w, http.StatusInternalServerError, errors.New("could not read the device before removal"))
		return
	}
	group, err := s.db.Group(d.GroupID)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	var warning string
	revoked := false
	if d.Enabled && groupAccessEnabled(group) && !isBypassMethod(d.Method) {
		if err := s.controlCredential(d, false); err != nil {
			if !discardableCredentialError(err) {
				fail(w, 400, err)
				return
			}
			warning = "Device removed. Its malformed credential could not be identified for revocation."
		} else {
			revoked = true
		}
	}
	lastBypassRoom := false
	if isBypassMethod(d.Method) {
		remaining, err := s.db.CountDevicesByGroupMethod(d.GroupID, d.Method)
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		lastBypassRoom = remaining == 1
		if lastBypassRoom {
			if err := s.removeBypassRoom(d.GroupID, d.Method, true); err != nil {
				fail(w, 400, fmt.Errorf("the device was not removed because its room could not be stopped: %w", err))
				return
			}
		}
	}
	if err := s.db.DeleteDevice(id); err != nil {
		if revoked {
			_ = s.controlCredential(d, true)
		}
		if lastBypassRoom && groupAccessEnabled(group) {
			_, _ = s.provisionCredential(d.GroupID, d.Name, d.Method)
		}
		fail(w, 400, err)
		return
	}
	result := map[string]any{"ok": true}
	if lastBypassRoom {
		if err := s.removeBypassRoom(d.GroupID, d.Method, false); err != nil {
			warning = "Device removed. Its stopped room data could not be cleared automatically: " + err.Error()
		}
	}
	if warning != "" {
		result["warning"] = warning
	}
	jsonOut(w, 200, result)
}

func discardableCredentialError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "failed to extract the uuid from the vless url") ||
		strings.Contains(message, "incomplete amneziawg client configuration")
}

func isBypassMethod(method string) bool {
	return strings.HasPrefix(method, "bypass-")
}

func uniqueBypassMethods(devices []store.Device) []string {
	seen := map[string]bool{}
	var methods []string
	for _, device := range devices {
		if isBypassMethod(device.Method) && !seen[device.Method] {
			seen[device.Method] = true
			methods = append(methods, device.Method)
		}
	}
	return methods
}

func (s *server) removeBypassRoom(groupID int64, method string, preserve bool) error {
	provider := strings.TrimPrefix(method, "bypass-")
	path := fmt.Sprintf("http://unix/v1/bypass/rooms/%d/%s", groupID, provider)
	if preserve {
		path += "?preserve=true"
	}
	req, _ := http.NewRequest(http.MethodDelete, path, nil)
	resp, err := s.agent.Do(req)
	if err != nil {
		return fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var result struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&result)
		if result.Error == "" {
			result.Error = "agent rejected room removal"
		}
		return errors.New(result.Error)
	}
	return nil
}

func (s *server) controlCredential(d store.Device, enabled bool) error {
	b, _ := json.Marshal(map[string]any{"Name": d.Name, "Method": d.Method, "Credential": d.Credential, "Enabled": enabled})
	req, _ := http.NewRequest("PUT", "http://unix/v1/credentials", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.agent.Do(req)
	if err != nil {
		return fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil || resp.StatusCode != http.StatusOK || !result.OK {
		if result.Error == "" {
			result.Error = "failed to apply credential state"
		}
		return errors.New(result.Error)
	}
	return nil
}

func (s *server) controlXrayCredentials(method string, devices []store.Device, enabled bool) error {
	credentials := make([]string, 0, len(devices))
	for _, device := range devices {
		credentials = append(credentials, device.Credential)
	}
	b, _ := json.Marshal(map[string]any{"Method": method, "Credentials": credentials, "Enabled": enabled})
	req, _ := http.NewRequest(http.MethodPut, "http://unix/v1/xray/credentials", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.agent.Do(req)
	if err != nil {
		return fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil || resp.StatusCode != http.StatusOK || !result.OK {
		if result.Error == "" {
			result.Error = "failed to apply Xray credential state"
		}
		return errors.New(result.Error)
	}
	return nil
}
func (s *server) discovery(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequest("GET", "http://unix/v1/discovery", nil)
	resp, err := s.agent.Do(req)
	if err != nil {
		fail(w, 502, fmt.Errorf("agent unavailable: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fail(w, 502, err)
		return
	}
	if components, ok := result["components"].([]any); ok {
		for _, raw := range components {
			component, _ := raw.(map[string]any)
			method := managedMethodByComponent[fmt.Sprint(component["id"])]
			if method == "" {
				continue
			}
			count, err := s.db.CountDevicesByMethod(method)
			if err == nil && count > 0 {
				component["can_uninstall"] = false
				component["note"] = fmt.Sprintf("Remove devices using this method first: %d.", count)
			}
		}
	}
	jsonOut(w, 200, result)
}

func (s *server) metrics(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequest("GET", "http://unix/v1/metrics", nil)
	resp, err := s.agent.Do(req)
	if err != nil {
		fail(w, 502, fmt.Errorf("agent unavailable: %w", err))
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		fail(w, 502, err)
		return
	}
	if resp.StatusCode == http.StatusOK {
		body = s.processTrafficMetrics(body)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

type bypassRoomSummary struct {
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
	Provider  string `json:"provider"`
	Code      string `json:"code"`
}

func nameBypassRooms(rooms []bypassRoomSummary, groups []store.Group) []bypassRoomSummary {
	names := make(map[int64]string, len(groups))
	for _, group := range groups {
		names[group.ID] = group.Name
	}
	for index := range rooms {
		rooms[index].GroupName = names[rooms[index].GroupID]
		if rooms[index].GroupName == "" {
			rooms[index].GroupName = fmt.Sprintf("Unknown group #%d", rooms[index].GroupID)
		}
	}
	sort.Slice(rooms, func(left, right int) bool {
		leftName := strings.ToLower(rooms[left].GroupName)
		rightName := strings.ToLower(rooms[right].GroupName)
		if leftName != rightName {
			return leftName < rightName
		}
		if rooms[left].Provider != rooms[right].Provider {
			return rooms[left].Provider < rooms[right].Provider
		}
		return rooms[left].Code < rooms[right].Code
	})
	return rooms
}

func (s *server) bypassRooms(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequest(http.MethodGet, "http://unix/v1/bypass/rooms", nil)
	resp, err := s.agent.Do(req)
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("agent unavailable: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	var result struct {
		Rooms []bypassRoomSummary `json:"rooms"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		fail(w, http.StatusBadGateway, errors.New("agent returned invalid room data"))
		return
	}
	groups, err := s.db.ListGroups()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "rooms": nameBypassRooms(result.Rooms, groups)})
}

func (s *server) updateInfo(w http.ResponseWriter, r *http.Request) {
	path, err := updateAgentPath(r.URL.RawQuery)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.proxyAgent(w, r, http.MethodGet, path)
}

func (s *server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	path, err := updateAgentPath(r.URL.RawQuery)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.proxyAgent(w, r, http.MethodPost, path)
}

func updateAgentPath(rawQuery string) (string, error) {
	if rawQuery == "" {
		return "/v1/update", nil
	}
	if rawQuery != "include_prereleases=1" {
		return "", errors.New("invalid update channel")
	}
	return "/v1/update?include_prereleases=1", nil
}

func (s *server) updateProgress(w http.ResponseWriter, r *http.Request) {
	s.proxyAgent(w, r, http.MethodGet, "/v1/update/progress")
}
func (s *server) installComponent(w http.ResponseWriter, r *http.Request) {
	s.proxyAgent(w, r, "POST", "/v1/components/"+r.PathValue("id")+"/install")
}
func (s *server) installStatus(w http.ResponseWriter, r *http.Request) {
	s.proxyAgent(w, r, "GET", "/v1/components/"+r.PathValue("id")+"/install")
}
func (s *server) uninstallComponent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if method := managedMethodByComponent[id]; method != "" {
		count, err := s.db.CountDevicesByMethod(method)
		if err != nil {
			fail(w, 500, err)
			return
		}
		if count > 0 {
			fail(w, 409, fmt.Errorf("remove devices using this method first: %d", count))
			return
		}
	}
	s.proxyAgent(w, r, "DELETE", "/v1/components/"+id)
}
func (s *server) proxyAgent(w http.ResponseWriter, r *http.Request, method, path string) {
	req, _ := http.NewRequest(method, "http://unix"+path, nil)
	resp, err := s.agent.Do(req)
	if err != nil {
		fail(w, 502, fmt.Errorf("agent unavailable: %w", err))
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
func (s *server) uploadBypass(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if provider != "wbstream" && provider != "telemost" && provider != "dion" && provider != "vk" {
		fail(w, 400, errors.New("unsupported provider"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := r.ParseMultipartForm(256 << 10); err != nil {
		fail(w, 400, errors.New("file is too large or invalid"))
		return
	}
	f, h, err := r.FormFile("cookies")
	if err != nil {
		fail(w, 400, errors.New("cookies JSON is required for this step"))
		return
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		fail(w, 400, err)
		return
	}
	var parsed any
	if json.Unmarshal(b, &parsed) != nil {
		fail(w, 400, errors.New("cookies file is not valid JSON"))
		return
	}
	req, _ := http.NewRequest("POST", "http://unix/v1/bypass/"+provider+"/credentials", strings.NewReader(string(b)))
	resp, err := s.agent.Do(req)
	if err != nil {
		fail(w, 502, fmt.Errorf("agent unavailable: %w", err))
		return
	}
	defer resp.Body.Close()
	var result struct {
		OK     bool   `json:"ok"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Bytes  int    `json:"bytes"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&result) != nil || !result.OK {
		fail(w, 502, errors.New("agent rejected the credentials file"))
		return
	}
	_, _ = s.db.DB.Exec(`INSERT INTO bypass_providers(provider,secret_path,status) VALUES(?,?,'credentials_ready') ON CONFLICT(provider) DO UPDATE SET secret_path=excluded.secret_path,status=excluded.status`, provider, result.Path)
	jsonOut(w, 201, map[string]any{"ok": true, "filename": filepath.Base(h.Filename), "sha256": result.SHA256, "bytes": result.Bytes})
}

func (s *server) clearBypass(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if provider != "wbstream" && provider != "telemost" && provider != "dion" && provider != "vk" {
		fail(w, 400, errors.New("unsupported provider"))
		return
	}
	req, _ := http.NewRequest("DELETE", "http://unix/v1/bypass/"+provider+"/credentials", nil)
	resp, err := s.agent.Do(req)
	if err != nil {
		fail(w, 502, fmt.Errorf("agent unavailable: %w", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	_, _ = s.db.DB.Exec(`INSERT INTO bypass_providers(provider,secret_path,status) VALUES(?, '', 'credentials_required') ON CONFLICT(provider) DO UPDATE SET secret_path='',status='credentials_required'`, provider)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, resp.Body)
}
