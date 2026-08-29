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
	amneziaWGProfileGeneration  = 3
)

var managedMethodByComponent = map[string]string{
	"xray": "xray", "xray-xhttp": "xray-xhttp", "amneziawg": "amneziawg", "bypass-wb": "bypass-wb",
	"bypass-telemost": "bypass-telemost", "bypass-dion": "bypass-dion", "bypass-vk": "bypass-vk",
}

type routingComponentSpec struct {
	method   string
	provider string
}

var routingComponentSpecs = map[string]routingComponentSpec{
	"bypass-wb":       {method: "bypass-wb", provider: "wbstream"},
	"bypass-telemost": {method: "bypass-telemost", provider: "telemost"},
	"bypass-dion":     {method: "bypass-dion", provider: "dion"},
	"bypass-vk":       {method: "bypass-vk", provider: "vk"},
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
	m.Handle("POST /api/components/amneziawg/update", admin(s.updateAmneziaWGComponent))
	m.Handle("GET /api/components/amneziawg/update", admin(s.updateAmneziaWGComponent))
	m.Handle("POST /api/components/{id}/profiles", admin(s.refreshComponentProfiles))
	m.Handle("DELETE /api/components/{id}", admin(s.uninstallComponent))
	m.Handle("DELETE /api/components/{id}/external", admin(s.removeExternalComponent))
	m.Handle("GET /api/components/{id}/install", auth(s.installStatus))
	m.Handle("GET /api/components/{id}/settings", admin(s.componentSettings))
	m.Handle("PUT /api/components/{id}/settings", admin(s.componentSettings))
	m.Handle("GET /api/components/docker/compose", admin(s.dockerCompose))
	m.Handle("POST /api/components/docker/compose", admin(s.dockerCompose))
	m.Handle("DELETE /api/components/docker/compose", admin(s.dockerCompose))
	m.Handle("DELETE /api/components/docker/compose/external", admin(s.dockerComposeExternal))
	m.Handle("GET /api/components/{id}/reality-sni", admin(s.xrayRealitySNI))
	m.Handle("POST /api/components/{id}/reality-sni", admin(s.xrayRealitySNI))
	m.Handle("PUT /api/components/{id}/reality-sni", admin(s.xrayRealitySNI))
	m.Handle("DELETE /api/components/{id}/reality-sni", admin(s.xrayRealitySNI))
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
		legacy := map[string]string{}
		for _, device := range devices {
			if !device.Enabled {
				continue
			}
			if isBypassMethod(device.Method) && device.ProfileGeneration < 1 {
				legacy[device.Method] = device.Credential
				continue
			}
			if !isBypassMethod(device.Method) || device.ProfileGeneration >= 1 {
				if err := s.controlCredential(device, true); err != nil {
					return fmt.Errorf("failed to restore %q: %w", device.Name, err)
				}
			}
		}
		for method, credential := range legacy {
			if err := s.restoreSharedBypassRoom(groupID, method, credential); err != nil {
				return fmt.Errorf("failed to restore the %s room: %w", strings.TrimPrefix(method, "bypass-"), err)
			}
		}
	} else {
		legacy := map[string]bool{}
		for _, device := range devices {
			if !device.Enabled {
				continue
			}
			if isBypassMethod(device.Method) && device.ProfileGeneration < 1 {
				legacy[device.Method] = true
				continue
			}
			if !isBypassMethod(device.Method) || device.ProfileGeneration >= 1 {
				if err := s.controlCredential(device, false); err != nil {
					return fmt.Errorf("failed to suspend %q: %w", device.Name, err)
				}
			}
		}
		for method := range legacy {
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
	legacyBypassMethods := map[string]string{}
	var deviceBypassRooms []store.Device
	for _, device := range devices {
		if isBypassMethod(device.Method) {
			if device.ProfileGeneration < 1 {
				legacyBypassMethods[device.Method] = device.Credential
			} else {
				if device.Enabled && groupAccessEnabled(group) {
					if err := s.controlCredential(device, false); err != nil {
						fail(w, 400, fmt.Errorf("failed to stop room %q: %w", device.Name, err))
						return
					}
				}
				deviceBypassRooms = append(deviceBypassRooms, device)
			}
			continue
		}
		if !device.Enabled {
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
	bypassMethods := make([]string, 0, len(legacyBypassMethods))
	for method := range legacyBypassMethods {
		bypassMethods = append(bypassMethods, method)
		if err := s.removeBypassRoom(id, method, true); err != nil {
			fail(w, 400, fmt.Errorf("failed to remove the dedicated bypass room: %w", err))
			return
		}
	}
	if err := s.db.DeleteGroup(id); err != nil {
		if groupAccessEnabled(group) {
			for _, device := range deviceBypassRooms {
				if device.Enabled {
					_ = s.controlCredential(device, true)
				}
			}
			for method, credential := range legacyBypassMethods {
				_ = s.restoreSharedBypassRoom(id, method, credential)
			}
		}
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
	for _, device := range deviceBypassRooms {
		if err := s.removeBypassDeviceRoom(device, false); err != nil {
			cleanupWarnings = append(cleanupWarnings, device.Name+": "+err.Error())
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
	id, err := s.db.ReserveDevice(gid, in.Name, in.Method, in.Format)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	profile, err := s.provisionCredential(id, gid, in.Name, in.Method)
	if err != nil {
		_ = s.db.DeleteDevice(id)
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.db.SetDeviceCredential(id, profile.Credential, profile.ProfileGeneration, profile.ProtocolVersion); err != nil {
		device := store.Device{ID: id, GroupID: gid, Name: in.Name, Method: in.Method, Format: in.Format, Credential: profile.Credential, ProfileGeneration: profile.ProfileGeneration, ProtocolVersion: profile.ProtocolVersion, Enabled: true}
		_ = s.controlCredential(device, false)
		if isBypassMethod(in.Method) {
			_ = s.removeBypassDeviceRoom(device, false)
		}
		_ = s.db.DeleteDevice(id)
		fail(w, http.StatusInternalServerError, fmt.Errorf("profile was prepared but could not be persisted: %w", err))
		return
	}
	device := store.Device{ID: id, GroupID: gid, Name: in.Name, Method: in.Method, Format: in.Format, Credential: profile.Credential, ProfileGeneration: profile.ProfileGeneration, ProtocolVersion: profile.ProtocolVersion, Enabled: true}
	group, groupErr := s.db.Group(gid)
	if groupErr == nil && !groupAccessEnabled(group) {
		var suspendErr error
		if isBypassMethod(in.Method) {
			suspendErr = s.removeBypassDeviceRoom(device, true)
		} else {
			suspendErr = s.controlCredential(device, false)
		}
		if suspendErr != nil {
			_ = s.db.DeleteDevice(id)
			fail(w, http.StatusBadGateway, fmt.Errorf("the expired group could not keep the new credential suspended: %w", suspendErr))
			return
		}
	}
	jsonOut(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "credential": displayCredential(device), "profile_generation": profile.ProfileGeneration, "protocol_version": profile.ProtocolVersion})
}

type renderedProfile struct {
	Credential        string `json:"credential"`
	ProfileGeneration int    `json:"profile_generation"`
	ProtocolVersion   string `json:"protocol_version"`
}

func (s *server) provisionCredential(deviceID, groupID int64, name, method string) (renderedProfile, error) {
	group, err := s.db.Group(groupID)
	if err != nil {
		return renderedProfile{}, fmt.Errorf("load credential group: %w", err)
	}
	profileName := managedProfileName(group.Name, name)
	payload, _ := json.Marshal(map[string]any{"name": profileName, "method": method, "group_id": groupID, "device_id": deviceID})
	req, _ := http.NewRequest("POST", "http://unix/v1/credentials", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.agent.Do(req)
	if err != nil {
		return renderedProfile{}, fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	var provisioned struct {
		OK bool `json:"ok"`
		renderedProfile
		Error string `json:"error"`
	}
	if json.NewDecoder(resp.Body).Decode(&provisioned) != nil || resp.StatusCode != http.StatusOK || !provisioned.OK {
		if provisioned.Error == "" {
			provisioned.Error = "failed to create a credential for the selected method"
		}
		return renderedProfile{}, errors.New(provisioned.Error)
	}
	if strings.TrimSpace(provisioned.Credential) == "" || provisioned.ProfileGeneration < 1 || strings.TrimSpace(provisioned.ProtocolVersion) == "" {
		return renderedProfile{}, errors.New("agent returned incomplete profile metadata")
	}
	return provisioned.renderedProfile, nil
}

func managedProfileName(groupName, deviceName string) string {
	return fmt.Sprintf("SBP · %s · %s", strings.TrimSpace(groupName), strings.TrimSpace(deviceName))
}

func (s *server) renderCredential(name, method, credential string) (renderedProfile, error) {
	payload, _ := json.Marshal(map[string]string{"name": name, "method": method, "credential": credential})
	req, _ := http.NewRequest(http.MethodPost, "http://unix/v1/credentials/render", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.agent.Do(req)
	if err != nil {
		return renderedProfile{}, fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	var rendered struct {
		OK bool `json:"ok"`
		renderedProfile
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rendered); err != nil || resp.StatusCode != http.StatusOK || !rendered.OK {
		if rendered.Error == "" {
			rendered.Error = "failed to render the current profile"
		}
		return renderedProfile{}, errors.New(rendered.Error)
	}
	if strings.TrimSpace(rendered.Credential) == "" || rendered.ProfileGeneration < 1 || strings.TrimSpace(rendered.ProtocolVersion) == "" {
		return renderedProfile{}, errors.New("agent returned incomplete profile metadata")
	}
	return rendered.renderedProfile, nil
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
	if strings.TrimSpace(in.Name) == "" {
		fail(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if err := s.db.UpdateDevice(id, current.GroupID, in.Name); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"ok": true})
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
	deviceBypassRoom := false
	if isBypassMethod(d.Method) {
		if d.ProfileGeneration >= 1 {
			deviceBypassRoom = true
			if err := s.removeBypassDeviceRoom(d, true); err != nil {
				fail(w, 400, fmt.Errorf("the device was not removed because its room could not be stopped: %w", err))
				return
			}
		} else {
			remaining, err := s.db.CountProfilesBeforeGeneration(d.GroupID, d.Method, 1)
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
	}
	if err := s.db.DeleteDevice(id); err != nil {
		if revoked {
			_ = s.controlCredential(d, true)
		}
		if deviceBypassRoom && groupAccessEnabled(group) {
			_ = s.controlCredential(d, true)
		}
		if lastBypassRoom && groupAccessEnabled(group) {
			_ = s.restoreSharedBypassRoom(d.GroupID, d.Method, d.Credential)
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
	if deviceBypassRoom {
		if err := s.removeBypassDeviceRoom(d, false); err != nil {
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

func (s *server) restoreSharedBypassRoom(groupID int64, method, credential string) error {
	provider := strings.TrimPrefix(method, "bypass-")
	payload, _ := json.Marshal(map[string]string{"credential": credential})
	path := fmt.Sprintf("http://unix/v1/bypass/rooms/%d/%s", groupID, provider)
	req, _ := http.NewRequest(http.MethodPut, path, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
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
			result.Error = "agent rejected shared room restore"
		}
		return errors.New(result.Error)
	}
	return nil
}

func (s *server) removeBypassDeviceRoom(device store.Device, preserve bool) error {
	provider := strings.TrimPrefix(device.Method, "bypass-")
	path := fmt.Sprintf("http://unix/v1/bypass/rooms/%d/%s/%d", device.GroupID, provider, device.ID)
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
			result.Error = "agent rejected device room removal"
		}
		return errors.New(result.Error)
	}
	return nil
}

func (s *server) controlCredential(d store.Device, enabled bool) error {
	b, _ := json.Marshal(map[string]any{"Name": d.Name, "Method": d.Method, "Credential": d.Credential, "Enabled": enabled, "group_id": d.GroupID, "device_id": d.ID})
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
	w.Header().Set("Cache-Control", "no-store")
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
		needsRoutingInventory := false
		for _, raw := range components {
			component, _ := raw.(map[string]any)
			_, routing := routingComponentSpecs[fmt.Sprint(component["id"])]
			installed, _ := component["installed"].(bool)
			external, _ := component["external"].(bool)
			if routing && installed && !external {
				needsRoutingInventory = true
				break
			}
		}
		var routingDevices []store.Device
		var routingRooms []bypassRoomSummary
		if needsRoutingInventory {
			var err error
			routingDevices, err = s.db.ListAllDevices()
			if err != nil {
				fail(w, http.StatusInternalServerError, err)
				return
			}
			routingRooms, err = s.loadBypassRooms()
			if err != nil {
				fail(w, http.StatusBadGateway, err)
				return
			}
		}
		for _, raw := range components {
			component, _ := raw.(map[string]any)
			id := fmt.Sprint(component["id"])
			method := managedMethodByComponent[id]
			if method == "" {
				continue
			}
			profileVersion, _ := component["profile_version"].(string)
			profileVersion = strings.TrimSpace(profileVersion)
			profileGenerationValue, _ := component["profile_generation"].(float64)
			profileGeneration := int(profileGenerationValue)
			installed, _ := component["installed"].(bool)
			external, _ := component["external"].(bool)
			canUpgrade, _ := component["can_update"].(bool)
			routingDrift := 0
			if spec, routing := routingComponentSpecs[id]; routing && installed && !external {
				routingDrift = routingRoomDrift(spec, routingDevices, routingRooms)
			}
			if routingDrift > 0 {
				component["can_update"] = true
				component["update_kind"] = "routing"
				component["profiles_to_update"] = routingDrift
			} else if canUpgrade {
				component["update_kind"] = "upgrade"
			} else if installed && !external && profileVersion != "" && profileGeneration > 0 {
				count, err := s.db.CountProfilesNotAtRevision(method, profileVersion, profileGeneration)
				if err != nil {
					fail(w, http.StatusInternalServerError, fmt.Errorf("inspect %s profile versions: %w", method, err))
					return
				}
				if count > 0 {
					if _, ok := componentProfileRefreshers[id]; ok {
						component["can_update"] = true
						component["update_kind"] = "profile"
						component["profiles_to_update"] = count
					}
				}
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
	GroupID    int64  `json:"group_id"`
	GroupName  string `json:"group_name"`
	DeviceID   int64  `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	Provider   string `json:"provider"`
	Code       string `json:"code"`
}

func (s *server) loadBypassRooms() ([]bypassRoomSummary, error) {
	req, _ := http.NewRequest(http.MethodGet, "http://unix/v1/bypass/rooms", nil)
	resp, err := s.agent.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("agent could not inspect routing rooms")
	}
	var result struct {
		Rooms []bypassRoomSummary `json:"rooms"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, errors.New("agent returned invalid room data")
	}
	return result.Rooms, nil
}

func routingRoomCode(value string) string {
	code := strings.TrimSpace(value)
	if before, _, found := strings.Cut(code, "#"); found {
		code = before
	}
	if before, _, found := strings.Cut(code, "?"); found {
		code = before
	}
	code = strings.TrimRight(code, "/")
	if index := strings.LastIndex(code, "/"); index >= 0 {
		code = code[index+1:]
	}
	if before, after, found := strings.Cut(code, "://"); found && before != "" {
		code = after
	}
	return strings.TrimSpace(code)
}

func classifyRoutingRooms(spec routingComponentSpec, devices []store.Device, rooms []bypassRoomSummary) (map[int64]bool, []bypassRoomSummary) {
	expected := map[int64]store.Device{}
	for _, device := range devices {
		if device.Method == spec.method {
			expected[device.ID] = device
		}
	}
	matched := map[int64]bool{}
	obsolete := make([]bypassRoomSummary, 0)
	for _, room := range rooms {
		if room.Provider != spec.provider {
			continue
		}
		device, ok := expected[room.DeviceID]
		if room.DeviceID < 1 || !ok || room.GroupID != device.GroupID || matched[room.DeviceID] ||
			routingRoomCode(room.Code) == "" || routingRoomCode(room.Code) != routingRoomCode(device.Credential) {
			obsolete = append(obsolete, room)
			continue
		}
		matched[room.DeviceID] = true
	}
	return matched, obsolete
}

func routingRoomDrift(spec routingComponentSpec, devices []store.Device, rooms []bypassRoomSummary) int {
	matched, obsolete := classifyRoutingRooms(spec, devices, rooms)
	drift := len(obsolete)
	for _, device := range devices {
		if device.Method == spec.method && !matched[device.ID] {
			drift++
		}
	}
	return drift
}

func nameBypassRooms(rooms []bypassRoomSummary, groups []store.Group, devices []store.Device) []bypassRoomSummary {
	names := make(map[int64]string, len(groups))
	for _, group := range groups {
		names[group.ID] = group.Name
	}
	deviceNames := make(map[int64]string, len(devices))
	for _, device := range devices {
		deviceNames[device.ID] = device.Name
	}
	for index := range rooms {
		rooms[index].GroupName = names[rooms[index].GroupID]
		if rooms[index].GroupName == "" {
			rooms[index].GroupName = fmt.Sprintf("Unknown group #%d", rooms[index].GroupID)
		}
		if rooms[index].DeviceID > 0 {
			rooms[index].DeviceName = deviceNames[rooms[index].DeviceID]
			if rooms[index].DeviceName == "" {
				rooms[index].DeviceName = fmt.Sprintf("Unknown device #%d", rooms[index].DeviceID)
			}
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
		if rooms[left].DeviceName != rooms[right].DeviceName {
			return strings.ToLower(rooms[left].DeviceName) < strings.ToLower(rooms[right].DeviceName)
		}
		return rooms[left].Code < rooms[right].Code
	})
	return rooms
}

func (s *server) bypassRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.loadBypassRooms()
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	groups, err := s.db.ListGroups()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	devices, err := s.db.ListAllDeviceMetadata()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "rooms": nameBypassRooms(rooms, groups, devices)})
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
	w.Header().Set("Cache-Control", "no-store")
	s.proxyAgent(w, r, "GET", "/v1/components/"+r.PathValue("id")+"/install")
}

type managedComponentProfileState struct {
	ID                string `json:"id"`
	Installed         bool   `json:"installed"`
	External          bool   `json:"external"`
	ProfileVersion    string `json:"profile_version"`
	ProfileGeneration int    `json:"profile_generation"`
}

func (s *server) componentProfileState(id string) (managedComponentProfileState, error) {
	req, _ := http.NewRequest(http.MethodGet, "http://unix/v1/discovery", nil)
	resp, err := s.agent.Do(req)
	if err != nil {
		return managedComponentProfileState{}, fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return managedComponentProfileState{}, errors.New("agent could not verify the managed component")
	}
	var result struct {
		Components []managedComponentProfileState `json:"components"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return managedComponentProfileState{}, errors.New("agent returned invalid component inventory")
	}
	for _, component := range result.Components {
		if component.ID == id {
			return component, nil
		}
	}
	return managedComponentProfileState{}, errors.New("unsupported component")
}

func profileConfigValue(body, section, key string) string {
	current := ""
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			current = trimmed
			continue
		}
		name, value, found := strings.Cut(trimmed, "=")
		if current == section && found && strings.EqualFold(strings.TrimSpace(name), key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func refreshAmneziaWGProfileMTU(credential string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(credential, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines)+1)
	inInterface := false
	interfaceSeen := false
	mtuSeen := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inInterface && !mtuSeen {
				result = append(result, "MTU = 1280")
				mtuSeen = true
			}
			inInterface = trimmed == "[Interface]"
			if inInterface {
				if interfaceSeen {
					return "", errors.New("AmneziaWG profile contains duplicate Interface sections")
				}
				interfaceSeen = true
			}
			result = append(result, line)
			continue
		}
		if inInterface {
			name, _, found := strings.Cut(trimmed, "=")
			if found && strings.EqualFold(strings.TrimSpace(name), "MTU") {
				if mtuSeen {
					return "", errors.New("AmneziaWG profile contains duplicate MTU values")
				}
				result = append(result, "MTU = 1280")
				mtuSeen = true
				continue
			}
		}
		result = append(result, line)
	}
	if inInterface && !mtuSeen {
		result = append(result, "MTU = 1280")
	}
	updated := strings.Join(result, "\n")
	for _, required := range []struct {
		section string
		key     string
	}{
		{section: "[Interface]", key: "Address"},
		{section: "[Interface]", key: "PrivateKey"},
		{section: "[Interface]", key: "HeaderProtectionKey"},
		{section: "[Peer]", key: "PublicKey"},
		{section: "[Peer]", key: "PresharedKey"},
		{section: "[Peer]", key: "Endpoint"},
	} {
		if profileConfigValue(updated, required.section, required.key) == "" {
			return "", errors.New("stored AmneziaWG 3.1 profile is incomplete")
		}
	}
	return updated, nil
}

type componentProfileRefresher struct {
	method      string
	agentRender bool
	transform   func(string) (string, error)
	result      func(int) string
}

var componentProfileRefreshers = map[string]componentProfileRefresher{
	"xray": {
		method:      "xray",
		agentRender: true,
		result: func(count int) string {
			return fmt.Sprintf("Refreshed %d Xray TCP profile(s) from current server settings. UUIDs and the running container were not changed.", count)
		},
	},
	"xray-xhttp": {
		method:      "xray-xhttp",
		agentRender: true,
		result: func(count int) string {
			return fmt.Sprintf("Refreshed %d Xray XHTTP profile(s) from current server settings. UUIDs and the running container were not changed.", count)
		},
	},
	"amneziawg": {
		method:    "amneziawg",
		transform: refreshAmneziaWGProfileMTU,
		result: func(count int) string {
			return fmt.Sprintf("Refreshed %d AmneziaWG profile(s) with MTU 1280. Server keys, peers, and container state were not changed.", count)
		},
	},
}

func (s *server) refreshComponentProfiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := strings.TrimSpace(r.PathValue("id"))
	refresher, ok := componentProfileRefreshers[id]
	routingSpec, routing := routingComponentSpecs[id]
	if !ok && !routing {
		fail(w, http.StatusBadRequest, errors.New("this component does not support profile refreshes"))
		return
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	component, err := s.componentProfileState(id)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if !component.Installed || component.External || strings.TrimSpace(component.ProfileVersion) == "" || component.ProfileGeneration < 1 {
		fail(w, http.StatusConflict, errors.New("the managed component is not installed or has no profile revision"))
		return
	}
	if routing {
		updated, err := s.reconcileRoutingComponent(component, routingSpec)
		if err != nil {
			fail(w, http.StatusConflict, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{
			"ok": true, "component_id": id, "protocol_version": component.ProfileVersion,
			"profile_generation": component.ProfileGeneration, "updated_profiles": updated,
			"output": fmt.Sprintf("Reconciled all %s rooms. %d device profile(s) received a current dedicated room.", id, updated),
		})
		return
	}
	devices, err := s.db.ListAllDevices()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	groups, err := s.db.ListGroups()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	groupNames := make(map[int64]string, len(groups))
	for _, group := range groups {
		groupNames[group.ID] = group.Name
	}
	updates := make([]store.DeviceProfileUpdate, 0)
	for _, device := range devices {
		if device.Method != refresher.method {
			continue
		}
		credential := ""
		if refresher.agentRender {
			groupName, ok := groupNames[device.GroupID]
			if !ok {
				fail(w, http.StatusConflict, fmt.Errorf("refresh profile for %s: group is missing", device.Name))
				return
			}
			profile, err := s.renderCredential(managedProfileName(groupName, device.Name), device.Method, device.Credential)
			if err != nil {
				fail(w, http.StatusConflict, fmt.Errorf("refresh profile for %s: %w", device.Name, err))
				return
			}
			if profile.ProfileGeneration != component.ProfileGeneration || strings.TrimSpace(profile.ProtocolVersion) != strings.TrimSpace(component.ProfileVersion) {
				fail(w, http.StatusConflict, fmt.Errorf("refresh profile for %s: agent rendered revision %s/%d instead of %s/%d", device.Name, profile.ProtocolVersion, profile.ProfileGeneration, component.ProfileVersion, component.ProfileGeneration))
				return
			}
			credential = profile.Credential
		} else {
			credential, err = refresher.transform(device.Credential)
			if err != nil {
				fail(w, http.StatusConflict, fmt.Errorf("refresh profile for %s: %w", device.Name, err))
				return
			}
		}
		updates = append(updates, store.DeviceProfileUpdate{
			DeviceID: device.ID, Name: device.Name, Credential: credential,
			ProfileGeneration: component.ProfileGeneration, ProtocolVersion: component.ProfileVersion,
		})
	}
	if err := s.db.UpdateDeviceProfiles(updates); err != nil {
		fail(w, http.StatusInternalServerError, fmt.Errorf("publish refreshed %s profiles: %w", refresher.method, err))
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{
		"ok": true, "component_id": id, "protocol_version": component.ProfileVersion,
		"profile_generation": component.ProfileGeneration, "updated_profiles": len(updates),
		"output": refresher.result(len(updates)),
	})
}

func (s *server) reconcileRoutingComponent(component managedComponentProfileState, spec routingComponentSpec) (int, error) {
	devices, err := s.db.ListAllDevices()
	if err != nil {
		return 0, err
	}
	rooms, err := s.loadBypassRooms()
	if err != nil {
		return 0, err
	}
	matched, _ := classifyRoutingRooms(spec, devices, rooms)
	groups, err := s.db.ListGroups()
	if err != nil {
		return 0, err
	}
	groupByID := make(map[int64]store.Group, len(groups))
	for _, group := range groups {
		groupByID[group.ID] = group
	}
	updated := 0
	for index := range devices {
		device := &devices[index]
		if device.Method != spec.method || matched[device.ID] {
			continue
		}
		profile, err := s.provisionCredential(device.ID, device.GroupID, device.Name, device.Method)
		if err != nil {
			return updated, fmt.Errorf("update stopped after %d profile(s): prepare %s: %w; retry Update to continue", updated, device.Name, err)
		}
		device.Credential = profile.Credential
		device.ProfileGeneration = profile.ProfileGeneration
		device.ProtocolVersion = profile.ProtocolVersion
		group, ok := groupByID[device.GroupID]
		if !ok {
			return updated, fmt.Errorf("update stopped after %d profile(s): group for %s is missing; retry Update after repairing the group", updated, device.Name)
		}
		if !device.Enabled || !groupAccessEnabled(group) {
			if err := s.controlCredential(*device, false); err != nil {
				return updated, fmt.Errorf("update stopped after %d profile(s): suspend %s: %w; retry Update to continue", updated, device.Name, err)
			}
		}
		if err := s.db.SetDeviceCredential(device.ID, profile.Credential, profile.ProfileGeneration, profile.ProtocolVersion); err != nil {
			return updated, fmt.Errorf("update stopped after %d profile(s): publish %s: %w; retry Update to continue", updated, device.Name, err)
		}
		updated++
	}

	currentDevices, err := s.db.ListAllDevices()
	if err != nil {
		return updated, err
	}
	currentRooms, err := s.loadBypassRooms()
	if err != nil {
		return updated, err
	}
	_, obsolete := classifyRoutingRooms(spec, currentDevices, currentRooms)
	for _, room := range obsolete {
		if room.DeviceID > 0 {
			device := store.Device{ID: room.DeviceID, GroupID: room.GroupID, Method: spec.method}
			if err := s.removeBypassDeviceRoom(device, false); err != nil {
				return updated, fmt.Errorf("current profiles are saved, but obsolete device room %d could not be removed: %w; retry Update to continue", room.DeviceID, err)
			}
			continue
		}
		if err := s.removeBypassRoom(room.GroupID, spec.method, false); err != nil {
			return updated, fmt.Errorf("current profiles are saved, but obsolete shared room for group %d could not be removed: %w; retry Update to continue", room.GroupID, err)
		}
	}
	if _, err := s.db.SetProfilesRevision(spec.method, component.ProfileVersion, component.ProfileGeneration); err != nil {
		return updated, fmt.Errorf("rooms are current, but profile revision could not be recorded: %w; retry Update to continue", err)
	}
	finalDevices, err := s.db.ListAllDevices()
	if err != nil {
		return updated, err
	}
	finalRooms, err := s.loadBypassRooms()
	if err != nil {
		return updated, err
	}
	if drift := routingRoomDrift(spec, finalDevices, finalRooms); drift != 0 {
		return updated, fmt.Errorf("%d routing room difference(s) remain; retry Update to continue", drift)
	}
	return updated, nil
}

type amneziaWGComponentUpdateProfile struct {
	DeviceID          int64  `json:"device_id"`
	Credential        string `json:"credential"`
	ProfileGeneration int    `json:"profile_generation"`
	ProtocolVersion   string `json:"protocol_version"`
}

type componentUpdateJob struct {
	ComponentID string `json:"component_id,omitempty"`
	Operation   string `json:"operation,omitempty"`
	Status      string `json:"status"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
}

type amneziaWGComponentUpdateAgentResponse struct {
	OK     bool               `json:"ok"`
	Job    componentUpdateJob `json:"job"`
	Result struct {
		Token   string `json:"token"`
		Devices []struct {
			DeviceID int64  `json:"device_id"`
			Name     string `json:"name"`
			Active   bool   `json:"active"`
		} `json:"devices"`
		Profiles []amneziaWGComponentUpdateProfile `json:"profiles"`
	} `json:"result"`
}

func (s *server) callAgentJSON(method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, _ := http.NewRequest(method, "http://unix"+path, body)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.agent.Do(req)
	if err != nil {
		return fmt.Errorf("agent unavailable: %w", err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 4<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(limited).Decode(&failure)
		if failure.Error == "" {
			failure.Error = "agent rejected the component update"
		}
		return errors.New(failure.Error)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, limited)
		return err
	}
	if err := json.NewDecoder(limited).Decode(output); err != nil {
		return errors.New("agent returned invalid component update data")
	}
	return nil
}

func (s *server) rollbackAmneziaWGComponentUpdate(token string) error {
	return s.callAgentJSON(http.MethodDelete, "/v1/components/amneziawg/update/"+url.PathEscape(token), nil, &map[string]any{})
}

func (s *server) updateAmneziaWGComponent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	if r.Method == http.MethodPost {
		devices, err := s.db.ListAllDeviceMetadata()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		groups, err := s.db.ListGroups()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		activeGroups := make(map[int64]bool, len(groups))
		for _, group := range groups {
			activeGroups[group.ID] = groupAccessEnabled(group)
		}
		requested := make([]map[string]any, 0)
		for _, device := range devices {
			if device.Method != "amneziawg" {
				continue
			}
			requested = append(requested, map[string]any{
				"device_id": device.ID,
				"name":      device.Name,
				"active":    device.Enabled && activeGroups[device.GroupID],
			})
		}
		var response amneziaWGComponentUpdateAgentResponse
		if err := s.callAgentJSON(http.MethodPost, "/v1/components/amneziawg/update", map[string]any{"devices": requested}, &response); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		jsonOut(w, http.StatusOK, response)
		return
	}

	var response amneziaWGComponentUpdateAgentResponse
	if err := s.callAgentJSON(http.MethodGet, "/v1/components/amneziawg/update", nil, &response); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if response.Job.Status != "done" || response.Result.Token == "" {
		jsonOut(w, http.StatusOK, response)
		return
	}
	if len(response.Result.Token) != 32 {
		fail(w, http.StatusBadGateway, errors.New("agent returned an invalid AmneziaWG update token"))
		return
	}
	current, err := s.db.ListAllDevices()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	oldProfiles := make([]store.DeviceProfileUpdate, 0)
	currentByID := make(map[int64]store.Device)
	groups, err := s.db.ListGroups()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	activeGroups := make(map[int64]bool, len(groups))
	for _, group := range groups {
		activeGroups[group.ID] = groupAccessEnabled(group)
	}
	for _, device := range current {
		if device.Method != "amneziawg" {
			continue
		}
		currentByID[device.ID] = device
		oldProfiles = append(oldProfiles, store.DeviceProfileUpdate{DeviceID: device.ID, Name: device.Name, Credential: device.Credential, ProfileGeneration: device.ProfileGeneration, ProtocolVersion: device.ProtocolVersion})
	}
	if len(response.Result.Profiles) != len(currentByID) || len(response.Result.Devices) != len(currentByID) {
		rollbackErr := s.rollbackAmneziaWGComponentUpdate(response.Result.Token)
		fail(w, http.StatusBadGateway, errors.Join(errors.New("AmneziaWG device set changed while the component update was running"), rollbackErr))
		return
	}
	updates := make([]store.DeviceProfileUpdate, 0, len(response.Result.Profiles))
	seen := make(map[int64]bool, len(response.Result.Profiles))
	requested := make(map[int64]struct {
		Name   string
		Active bool
	}, len(response.Result.Devices))
	for _, device := range response.Result.Devices {
		if _, duplicate := requested[device.DeviceID]; duplicate {
			rollbackErr := s.rollbackAmneziaWGComponentUpdate(response.Result.Token)
			fail(w, http.StatusBadGateway, errors.Join(errors.New("agent returned a duplicate AmneziaWG device set"), rollbackErr))
			return
		}
		requested[device.DeviceID] = struct {
			Name   string
			Active bool
		}{Name: device.Name, Active: device.Active}
	}
	for _, profile := range response.Result.Profiles {
		device, ok := currentByID[profile.DeviceID]
		expected, requestedOK := requested[profile.DeviceID]
		active := device.Enabled && activeGroups[device.GroupID]
		if !ok || !requestedOK || expected.Name != device.Name || expected.Active != active || seen[profile.DeviceID] || strings.TrimSpace(profile.Credential) == "" || profile.ProfileGeneration != amneziaWGProfileGeneration || profile.ProtocolVersion != "3.1" {
			rollbackErr := s.rollbackAmneziaWGComponentUpdate(response.Result.Token)
			fail(w, http.StatusBadGateway, errors.Join(errors.New("agent returned an incomplete AmneziaWG profile set"), rollbackErr))
			return
		}
		seen[profile.DeviceID] = true
		updates = append(updates, store.DeviceProfileUpdate{DeviceID: device.ID, Name: device.Name, Credential: profile.Credential, ProfileGeneration: profile.ProfileGeneration, ProtocolVersion: profile.ProtocolVersion})
	}
	if err := s.db.UpdateDeviceProfiles(updates); err != nil {
		rollbackErr := s.rollbackAmneziaWGComponentUpdate(response.Result.Token)
		fail(w, http.StatusInternalServerError, errors.Join(fmt.Errorf("publish new AmneziaWG profiles: %w", err), rollbackErr))
		return
	}
	commitPath := "/v1/components/amneziawg/update/" + url.PathEscape(response.Result.Token) + "/commit"
	if err := s.callAgentJSON(http.MethodPost, commitPath, nil, &map[string]any{}); err != nil {
		rollbackErr := s.rollbackAmneziaWGComponentUpdate(response.Result.Token)
		if rollbackErr == nil {
			if restoreErr := s.db.UpdateDeviceProfiles(oldProfiles); restoreErr != nil {
				fail(w, http.StatusInternalServerError, errors.Join(err, fmt.Errorf("restore previous AmneziaWG profiles: %w", restoreErr)))
				return
			}
		}
		fail(w, http.StatusBadGateway, errors.Join(fmt.Errorf("commit AmneziaWG component update: %w", err), rollbackErr))
		return
	}
	response.Job.Output = fmt.Sprintf("AmneziaWG 3.1 installed and %d device profile(s) reissued", len(updates))
	response.Result.Token = ""
	response.Result.Devices = nil
	response.Result.Profiles = nil
	jsonOut(w, http.StatusOK, response)
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

func (s *server) removeExternalComponent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != "tweaks" && id != "docker" {
		fail(w, http.StatusBadRequest, errors.New("external removal is not supported for this component"))
		return
	}
	s.proxyAgent(w, r, http.MethodDelete, "/v1/components/"+id+"/external")
}

func (s *server) dockerCompose(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.proxyAgent(w, r, r.Method, "/v1/components/docker/compose")
}

func (s *server) dockerComposeExternal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.proxyAgent(w, r, http.MethodDelete, "/v1/components/docker/compose/external")
}

func (s *server) componentSettings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != "tweaks" && id != "amneziawg" {
		fail(w, http.StatusBadRequest, errors.New("editable server settings are not available for this component"))
		return
	}
	var body []byte
	if r.Method == http.MethodPut {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, 32<<10+1))
		if err != nil || len(body) == 0 || len(body) > 32<<10 {
			fail(w, http.StatusBadRequest, errors.New("invalid component settings request size"))
			return
		}
	}
	req, _ := http.NewRequest(r.Method, "http://unix/v1/components/"+id+"/settings", bytes.NewReader(body))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.agent.Do(req)
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("agent unavailable: %w", err))
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *server) xrayRealitySNI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != "xray" && id != "xray-xhttp" {
		fail(w, http.StatusBadRequest, errors.New("REALITY settings are available only for Xray components"))
		return
	}
	var body []byte
	if r.Method != http.MethodGet {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, 16<<10+1))
		if err != nil || len(body) == 0 || len(body) > 16<<10 {
			fail(w, http.StatusBadRequest, errors.New("invalid REALITY settings request size"))
			return
		}
	}
	req, _ := http.NewRequest(r.Method, "http://unix/v1/components/"+id+"/reality-sni", bytes.NewReader(body))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.agent.Do(req)
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("agent unavailable: %w", err))
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
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
