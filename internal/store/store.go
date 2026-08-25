package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }

type groupQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type Account struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	Role    string `json:"role"`
	GroupID *int64 `json:"group_id,omitempty"`
}

type Group struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Contact       string  `json:"contact"`
	Status        string  `json:"status"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
	Unlimited     bool    `json:"unlimited"`
	RXBytes       int64   `json:"rx_bytes"`
	TXBytes       int64   `json:"tx_bytes"`
	AccessApplied bool    `json:"-"`
}

type Device struct {
	ID                int64  `json:"id"`
	GroupID           int64  `json:"group_id"`
	Name              string `json:"name"`
	Method            string `json:"method"`
	Format            string `json:"format,omitempty"`
	Credential        string `json:"credential,omitempty"`
	ProfileGeneration int    `json:"profile_generation"`
	ProtocolVersion   string `json:"protocol_version"`
	Enabled           bool   `json:"enabled"`
	LastSeenAt        string `json:"last_seen_at,omitempty"`
	RXBytes           int64  `json:"rx_bytes"`
	TXBytes           int64  `json:"tx_bytes"`
}

type DeviceProfileUpdate struct {
	DeviceID          int64
	Name              string
	Credential        string
	ProfileGeneration int
	ProtocolVersion   string
}

type DeviceTrafficSample struct {
	DeviceID int64
	GroupID  int64
	Protocol string
	RXBytes  int64
	TXBytes  int64
}

const deviceSelect = `SELECT d.id,d.group_id,d.name,d.method,d.credential_format,d.enabled,COALESCE(d.last_seen_at,''),COALESCE(t.rx_bytes,0),COALESCE(t.tx_bytes,0),COALESCE(CAST(c.secret_blob AS TEXT),''),COALESCE(c.profile_generation,0),COALESCE(c.protocol_version,'') FROM devices d LEFT JOIN traffic_current t ON t.scope_type='device' AND t.scope_id=d.id AND t.protocol='all' AND t.month_key=strftime('%Y-%m','now') LEFT JOIN credentials c ON c.device_id=d.id AND c.protocol=d.method`
const deviceMetadataSelect = `SELECT d.id,d.group_id,d.name,d.method,d.credential_format,d.enabled,COALESCE(d.last_seen_at,''),COALESCE(t.rx_bytes,0),COALESCE(t.tx_bytes,0),'',COALESCE(c.profile_generation,0),COALESCE(c.protocol_version,'') FROM devices d LEFT JOIN traffic_current t ON t.scope_type='device' AND t.scope_id=d.id AND t.protocol='all' AND t.month_key=strftime('%Y-%m','now') LEFT JOIN credentials c ON c.device_id=d.id AND c.protocol=d.method`
const maxAccountSessions = 64

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDevice(row rowScanner) (Device, error) {
	var d Device
	var enabled int
	err := row.Scan(&d.ID, &d.GroupID, &d.Name, &d.Method, &d.Format, &enabled, &d.LastSeenAt, &d.RXBytes, &d.TXBytes, &d.Credential, &d.ProfileGeneration, &d.ProtocolVersion)
	d.Enabled = enabled == 1
	return d, err
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=wal_autocheckpoint(256)&_pragma=journal_size_limit(4194304)&_pragma=max_page_count(65536)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema() error {
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS accounts (
 id INTEGER PRIMARY KEY, login TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('owner','admin','user')), group_id INTEGER,
 enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS one_owner ON accounts(role) WHERE role='owner';
CREATE TABLE IF NOT EXISTS groups (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL, contact TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active',
 expires_at TEXT, unlimited INTEGER NOT NULL DEFAULT 0, access_applied INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
 id INTEGER PRIMARY KEY, group_id INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
 name TEXT NOT NULL, method TEXT NOT NULL DEFAULT 'xray', credential_format TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, last_seen_at TEXT, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS credentials (
 id INTEGER PRIMARY KEY, device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
 protocol TEXT NOT NULL, public_id TEXT NOT NULL, secret_blob BLOB, enabled INTEGER NOT NULL DEFAULT 1,
 profile_generation INTEGER NOT NULL DEFAULT 0, protocol_version TEXT NOT NULL DEFAULT '',
 UNIQUE(device_id, protocol)
);
CREATE TABLE IF NOT EXISTS traffic_current (
 scope_type TEXT NOT NULL, scope_id INTEGER NOT NULL, protocol TEXT NOT NULL,
 month_key TEXT NOT NULL, rx_bytes INTEGER NOT NULL DEFAULT 0, tx_bytes INTEGER NOT NULL DEFAULT 0,
 last_raw_rx INTEGER NOT NULL DEFAULT 0, last_raw_tx INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(scope_type, scope_id, protocol)
);
CREATE TABLE IF NOT EXISTS sessions (
 token_hash TEXT PRIMARY KEY, account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 csrf TEXT NOT NULL, expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS bypass_providers (
 provider TEXT PRIMARY KEY, version TEXT NOT NULL DEFAULT '', secret_path TEXT NOT NULL DEFAULT '',
 room_link TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'not_installed'
);
`)
	if err != nil {
		return err
	}
	return s.reconcileCredentialColumns()
}

// reconcileCredentialColumns compares the live table with the current desired
// shape. It deliberately avoids a release-by-release migration chain.
func (s *Store) reconcileCredentialColumns() error {
	rows, err := s.DB.Query(`PRAGMA table_info(credentials)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, definition := range []struct {
		name string
		sql  string
	}{
		{"profile_generation", `ALTER TABLE credentials ADD COLUMN profile_generation INTEGER NOT NULL DEFAULT 0`},
		{"protocol_version", `ALTER TABLE credentials ADD COLUMN protocol_version TEXT NOT NULL DEFAULT ''`},
	} {
		if columns[definition.name] {
			continue
		}
		if _, err := s.DB.Exec(definition.sql); err != nil {
			return fmt.Errorf("add credentials.%s: %w", definition.name, err)
		}
	}
	return nil
}

func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func HashToken(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }

func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", errors.New("password must contain at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[3])
	expected, e2 := base64.RawStdEncoding.DecodeString(parts[4])
	if e1 != nil || e2 != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(expected)))
	var diff byte
	for i := range actual {
		diff |= actual[i] ^ expected[i]
	}
	return diff == 0
}

func (s *Store) OwnerExists() (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM accounts WHERE role='owner'`).Scan(&n)
	return n > 0, err
}

func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) CreateOwner(login, password string) error {
	login = strings.TrimSpace(login)
	if len(login) < 3 {
		return errors.New("login is too short")
	}
	ph, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO accounts(login,password_hash,role,created_at) VALUES(?,?,'owner',?)`, login, ph, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) Authenticate(login, password string) (Account, error) {
	var a Account
	var ph string
	var enabled int
	var gid sql.NullInt64
	err := s.DB.QueryRow(`SELECT id,login,password_hash,role,group_id,enabled FROM accounts WHERE login=?`, strings.TrimSpace(login)).Scan(&a.ID, &a.Login, &ph, &a.Role, &gid, &enabled)
	if err != nil || enabled != 1 || !VerifyPassword(ph, password) {
		return Account{}, errors.New("invalid credentials")
	}
	if gid.Valid {
		a.GroupID = &gid.Int64
	}
	return a, nil
}

func (s *Store) ChangeOwnerPassword(currentPassword, newPassword string) error {
	var id int64
	var currentHash string
	if err := s.DB.QueryRow(`SELECT id,password_hash FROM accounts WHERE login='admin' AND role='owner' AND enabled=1`).Scan(&id, &currentHash); err != nil || !VerifyPassword(currentHash, currentPassword) {
		return errors.New("current password is incorrect")
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE accounts SET password_hash=? WHERE id=?`, newHash, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateSession(accountID int64) (string, string, error) {
	token, err := RandomToken(32)
	if err != nil {
		return "", "", err
	}
	csrf, err := RandomToken(24)
	if err != nil {
		return "", "", err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err := tx.Exec(`DELETE FROM sessions WHERE expires_at<=?`, now.Format(time.RFC3339)); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`INSERT INTO sessions(token_hash,account_id,csrf,expires_at) VALUES(?,?,?,?)`, HashToken(token), accountID, csrf, now.Add(24*time.Hour).Format(time.RFC3339)); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE account_id=? AND rowid NOT IN (SELECT rowid FROM sessions WHERE account_id=? ORDER BY rowid DESC LIMIT ?)`, accountID, accountID, maxAccountSessions); err != nil {
		return "", "", err
	}
	return token, csrf, tx.Commit()
}

func (s *Store) Session(token string) (Account, string, error) {
	var a Account
	var csrf string
	var gid sql.NullInt64
	err := s.DB.QueryRow(`SELECT a.id,a.login,a.role,a.group_id,s.csrf FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE s.token_hash=? AND s.expires_at>? AND a.enabled=1`, HashToken(token), time.Now().UTC().Format(time.RFC3339)).Scan(&a.ID, &a.Login, &a.Role, &gid, &csrf)
	if gid.Valid {
		a.GroupID = &gid.Int64
	}
	return a, csrf, err
}

func (s *Store) DeleteSession(token string) {
	_, _ = s.DB.Exec(`DELETE FROM sessions WHERE token_hash=?`, HashToken(token))
}

func (s *Store) ListGroups() ([]Group, error) {
	rows, err := s.DB.Query(`SELECT g.id,g.name,g.contact,CASE WHEN g.unlimited=0 AND datetime(g.expires_at)<=datetime('now') THEN 'expired' ELSE g.status END,g.expires_at,g.unlimited,COALESCE(t.rx_bytes,0),COALESCE(t.tx_bytes,0),g.access_applied FROM groups g LEFT JOIN traffic_current t ON t.scope_type='group' AND t.scope_id=g.id AND t.protocol='all' AND t.month_key=strftime('%Y-%m','now')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		var exp sql.NullString
		var unlimited, accessApplied int
		if err := rows.Scan(&g.ID, &g.Name, &g.Contact, &g.Status, &exp, &unlimited, &g.RXBytes, &g.TXBytes, &accessApplied); err != nil {
			return nil, err
		}
		if exp.Valid {
			g.ExpiresAt = &exp.String
		}
		g.Unlimited = unlimited == 1
		g.AccessApplied = accessApplied == 1
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return nameBefore(out[i].Name, out[i].ID, out[j].Name, out[j].ID) })
	return out, nil
}

func (s *Store) Group(id int64) (Group, error) {
	var g Group
	var exp sql.NullString
	var unlimited, accessApplied int
	err := s.DB.QueryRow(`SELECT id,name,contact,CASE WHEN unlimited=0 AND datetime(expires_at)<=datetime('now') THEN 'expired' ELSE status END,expires_at,unlimited,access_applied FROM groups WHERE id=?`, id).Scan(&g.ID, &g.Name, &g.Contact, &g.Status, &exp, &unlimited, &accessApplied)
	if err != nil {
		return Group{}, err
	}
	if exp.Valid {
		g.ExpiresAt = &exp.String
	}
	g.Unlimited = unlimited == 1
	g.AccessApplied = accessApplied == 1
	return g, nil
}

func (s *Store) SetGroupAccessApplied(id int64, active bool) error {
	result, err := s.DB.Exec(`UPDATE groups SET access_applied=? WHERE id=?`, boolInt(active), id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return errors.New("group not found")
	}
	return nil
}

func (s *Store) FindGroupByName(name string) (Group, error) {
	name, err := NormalizeGroupName(name)
	if err != nil {
		return Group{}, err
	}
	rows, err := s.DB.Query(`SELECT id,name,CASE WHEN unlimited=0 AND datetime(expires_at)<=datetime('now') THEN 'expired' ELSE status END,expires_at,unlimited FROM groups ORDER BY id DESC`)
	if err != nil {
		return Group{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var g Group
		var exp sql.NullString
		var unlimited int
		if err := rows.Scan(&g.ID, &g.Name, &g.Status, &exp, &unlimited); err != nil {
			return Group{}, err
		}
		storedName, normalizeErr := NormalizeGroupName(g.Name)
		if normalizeErr != nil || !strings.EqualFold(storedName, name) {
			continue
		}
		if exp.Valid {
			g.ExpiresAt = &exp.String
		}
		g.Unlimited = unlimited == 1
		return g, nil
	}
	if err := rows.Err(); err != nil {
		return Group{}, err
	}
	return Group{}, sql.ErrNoRows
}

// NormalizeGroupName produces the display name and the canonical form used by
// public /check links. Underscores intentionally represent spaces in URLs.
func NormalizeGroupName(name string) (string, error) {
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > 160 {
		return "", errors.New("group name must contain between 1 and 160 characters")
	}
	hasLetterOrDigit := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			hasLetterOrDigit = true
		case r == ' ' || r == '-':
		default:
			return "", errors.New("group name may contain only letters, numbers, spaces, and hyphens")
		}
	}
	if !hasLetterOrDigit {
		return "", errors.New("group name must contain at least one letter or number")
	}
	return name, nil
}

func groupNameExists(q groupQuerier, name string, excludeID int64) (bool, error) {
	rows, err := q.Query(`SELECT id,name FROM groups`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var stored string
		if err := rows.Scan(&id, &stored); err != nil {
			return false, err
		}
		if id == excludeID {
			continue
		}
		normalized, err := NormalizeGroupName(stored)
		if err == nil && strings.EqualFold(normalized, name) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) CreateGroupWithExpiration(name, contact string, days int, isUnlimited bool, expiresAt string) (int64, error) {
	name, err := NormalizeGroupName(name)
	if err != nil {
		return 0, err
	}
	contact = strings.TrimSpace(contact)
	if days < 1 || days > 3650 {
		days = 30
	}
	var exp any = time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	if isUnlimited {
		exp = nil
	} else if strings.TrimSpace(expiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return 0, errors.New("expiration must be an RFC3339 date")
		}
		exp = parsed.UTC().Format(time.RFC3339)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	taken, err := groupNameExists(tx, name, 0)
	if err != nil {
		return 0, err
	}
	if taken {
		return 0, errors.New("a group with this name already exists")
	}
	r, err := tx.Exec(`INSERT INTO groups(name,contact,status,expires_at,unlimited,created_at) VALUES(?,?,'active',?,?,?)`, name, contact, exp, boolInt(isUnlimited), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) UpdateGroupDetails(id int64, name, contact, expiresAt string, unlimited bool) error {
	name, err := NormalizeGroupName(name)
	if err != nil {
		return err
	}
	var exp any
	if !unlimited {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return errors.New("expiration must be an RFC3339 date")
		}
		exp = parsed.UTC().Format(time.RFC3339)
	}
	contact = strings.TrimSpace(contact)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	taken, err := groupNameExists(tx, name, id)
	if err != nil {
		return err
	}
	if taken {
		return errors.New("a group with this name already exists")
	}
	r, err := tx.Exec(`UPDATE groups SET name=?,contact=?,expires_at=?,unlimited=?,status=CASE WHEN ?=1 OR datetime(?)>datetime('now') THEN 'active' ELSE 'expired' END WHERE id=?`, name, contact, exp, boolInt(unlimited), boolInt(unlimited), exp, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return errors.New("group not found")
	}
	return tx.Commit()
}

func (s *Store) ExtendGroupMonth(id int64) error {
	r, err := s.DB.Exec(`UPDATE groups SET expires_at=strftime('%Y-%m-%dT%H:%M:%SZ',CASE WHEN expires_at IS NULL OR datetime(expires_at)<datetime('now') THEN datetime('now','+1 month') ELSE datetime(expires_at,'+1 month') END), status='active' WHERE id=? AND unlimited=0`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return errors.New("paid group not found")
	}
	return nil
}

func (s *Store) DeleteGroup(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM traffic_current WHERE scope_type='device' AND scope_id IN (SELECT id FROM devices WHERE group_id=?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM traffic_current WHERE scope_type='group' AND scope_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE accounts SET group_id=NULL WHERE group_id=?`, id); err != nil {
		return err
	}
	// Delete the complete group tree explicitly instead of relying only on
	// SQLite foreign-key cascades. This keeps deletion correct for databases
	// created by older builds or opened by tools that did not enable FKs.
	if _, err := tx.Exec(`DELETE FROM credentials WHERE device_id IN (SELECT id FROM devices WHERE group_id=?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM devices WHERE group_id=?`, id); err != nil {
		return err
	}
	r, err := tx.Exec(`DELETE FROM groups WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return errors.New("group not found")
	}
	return tx.Commit()
}

func (s *Store) ListDevices(groupID int64) ([]Device, error) {
	return s.listDevices(deviceSelect+` WHERE d.group_id=?`, groupID)
}

func (s *Store) ListAllDevices() ([]Device, error) {
	return s.listDevices(deviceSelect)
}

// ListDeviceMetadata returns devices without reading their credentials.
func (s *Store) ListDeviceMetadata(groupID int64) ([]Device, error) {
	return s.listDevices(deviceMetadataSelect+` WHERE d.group_id=?`, groupID)
}

// ListAllDeviceMetadata returns all devices without reading their credentials.
func (s *Store) ListAllDeviceMetadata() ([]Device, error) {
	return s.listDevices(deviceMetadataSelect)
}

func (s *Store) listDevices(query string, args ...any) ([]Device, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Device, 0)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		return nameBefore(out[i].Name, out[i].ID, out[j].Name, out[j].ID)
	})
	return out, nil
}

func nameBefore(a string, aID int64, b string, bID int64) bool {
	aFolded, bFolded := strings.ToLower(a), strings.ToLower(b)
	if aFolded != bFolded {
		return aFolded < bFolded
	}
	if a != b {
		return a < b
	}
	return aID < bID
}

func (s *Store) CountDevicesByMethod(method string) (int, error) {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM devices WHERE method=?`, method).Scan(&count)
	return count, err
}

func (s *Store) CountProfilesNotAtVersion(method, version string) (int, error) {
	method = strings.TrimSpace(method)
	version = strings.TrimSpace(version)
	if method == "" || version == "" {
		return 0, errors.New("profile method and version are required")
	}
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM credentials WHERE protocol=? AND protocol_version<>?`, method, version).Scan(&count)
	return count, err
}

// SetProfilesVersion records trusted component metadata without rewriting any
// credential material. One UPDATE keeps the complete component scope atomic.
func (s *Store) SetProfilesVersion(method, version string) (int64, error) {
	method = strings.TrimSpace(method)
	version = strings.TrimSpace(version)
	if method == "" || version == "" {
		return 0, errors.New("profile method and version are required")
	}
	result, err := s.DB.Exec(`UPDATE credentials SET protocol_version=? WHERE protocol=? AND protocol_version<>?`, version, method, version)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) CountProfilesBeforeGeneration(groupID int64, method string, generation int) (int, error) {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM devices d LEFT JOIN credentials c ON c.device_id=d.id AND c.protocol=d.method WHERE d.group_id=? AND d.method=? AND COALESCE(c.profile_generation,0)<?`, groupID, method, generation).Scan(&count)
	return count, err
}

func (s *Store) SetGroupProtocolTraffic(groupID int64, protocol, month string, rx, tx int64) error {
	if groupID < 1 || strings.TrimSpace(protocol) == "" || strings.TrimSpace(month) == "" || rx < 0 || tx < 0 {
		return errors.New("invalid traffic sample")
	}
	txn, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer txn.Rollback()
	if _, err := txn.Exec(`DELETE FROM traffic_current WHERE scope_type='group' AND scope_id=? AND month_key<>?`, groupID, month); err != nil {
		return err
	}
	if _, err := txn.Exec(`INSERT INTO traffic_current(scope_type,scope_id,protocol,month_key,rx_bytes,tx_bytes) VALUES('group',?,?,?,?,?) ON CONFLICT(scope_type,scope_id,protocol) DO UPDATE SET month_key=excluded.month_key,rx_bytes=excluded.rx_bytes,tx_bytes=excluded.tx_bytes`, groupID, protocol, month, rx, tx); err != nil {
		return err
	}
	if err := refreshGroupTraffic(txn, groupID, month); err != nil {
		return err
	}
	return txn.Commit()
}

func (s *Store) SetDeviceTrafficSamples(month string, samples []DeviceTrafficSample) error {
	month = strings.TrimSpace(month)
	if month == "" {
		return errors.New("traffic month is required")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM traffic_current WHERE month_key<>?`, month); err != nil {
		return err
	}
	if len(samples) == 0 {
		return tx.Commit()
	}
	type groupMethod struct {
		groupID int64
		method  string
	}
	affected := map[groupMethod]bool{}
	for _, sample := range samples {
		if sample.DeviceID < 1 || sample.GroupID < 1 || !validMethod(sample.Protocol) || sample.RXBytes < 0 || sample.TXBytes < 0 {
			return errors.New("invalid device traffic sample")
		}
		if _, err := tx.Exec(`INSERT INTO traffic_current(scope_type,scope_id,protocol,month_key,rx_bytes,tx_bytes) VALUES('device',?,?,?,?,?) ON CONFLICT(scope_type,scope_id,protocol) DO UPDATE SET month_key=excluded.month_key,rx_bytes=excluded.rx_bytes,tx_bytes=excluded.tx_bytes`, sample.DeviceID, "all", month, sample.RXBytes, sample.TXBytes); err != nil {
			return err
		}
		affected[groupMethod{groupID: sample.GroupID, method: sample.Protocol}] = true
	}
	for key := range affected {
		if err := refreshGroupMethodTraffic(tx, key.groupID, key.method, month); err != nil {
			return err
		}
		if err := refreshGroupTraffic(tx, key.groupID, month); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func refreshGroupMethodTraffic(tx *sql.Tx, groupID int64, method, month string) error {
	_, err := tx.Exec(`INSERT INTO traffic_current(scope_type,scope_id,protocol,month_key,rx_bytes,tx_bytes)
		SELECT 'group',?,?,?,COALESCE(SUM(t.rx_bytes),0),COALESCE(SUM(t.tx_bytes),0)
		FROM devices d LEFT JOIN traffic_current t ON t.scope_type='device' AND t.scope_id=d.id AND t.protocol='all' AND t.month_key=?
		WHERE d.group_id=? AND d.method=?
		ON CONFLICT(scope_type,scope_id,protocol) DO UPDATE SET month_key=excluded.month_key,rx_bytes=excluded.rx_bytes,tx_bytes=excluded.tx_bytes`, groupID, method, month, month, groupID, method)
	return err
}

func refreshGroupTraffic(tx *sql.Tx, groupID int64, month string) error {
	_, err := tx.Exec(`INSERT INTO traffic_current(scope_type,scope_id,protocol,month_key,rx_bytes,tx_bytes)
		SELECT 'group',?,'all',?,COALESCE(SUM(rx_bytes),0),COALESCE(SUM(tx_bytes),0)
		FROM traffic_current WHERE scope_type='group' AND scope_id=? AND protocol<>'all' AND month_key=?
		ON CONFLICT(scope_type,scope_id,protocol) DO UPDATE SET month_key=excluded.month_key,rx_bytes=excluded.rx_bytes,tx_bytes=excluded.tx_bytes`, groupID, month, groupID, month)
	return err
}

func validMethod(method string) bool {
	switch method {
	case "xray", "xray-xhttp", "amneziawg", "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		return true
	}
	return false
}

func (s *Store) ValidateDevice(groupID int64, name, method, format string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if !validMethod(strings.TrimSpace(method)) {
		return errors.New("unsupported device method")
	}
	if method == "amneziawg" && format != "" && format != "native" && format != "app" {
		return errors.New("unsupported AmneziaWG format")
	}
	var exists int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM groups WHERE id=?`, groupID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return errors.New("group not found")
	}
	return nil
}

// MatchingDevice returns an existing device with the same user-visible
// identity. Device creation calls this while holding the credential lock so a
// repeated browser request cannot provision a second VPN credential.
func (s *Store) MatchingDevice(groupID int64, name, method, format string) (Device, bool, error) {
	name = strings.TrimSpace(name)
	rows, err := s.DB.Query(deviceSelect+` WHERE d.group_id=? AND d.method=? AND d.credential_format=? ORDER BY d.id`, groupID, strings.TrimSpace(method), strings.TrimSpace(format))
	if err != nil {
		return Device{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return Device{}, false, err
		}
		if strings.EqualFold(strings.TrimSpace(device.Name), name) {
			return device, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return Device{}, false, err
	}
	return Device{}, false, nil
}

func (s *Store) CreateDevice(groupID int64, name string, args ...string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("name is required")
	}
	method := "xray"
	credential := ""
	format := ""
	if len(args) > 0 {
		method = strings.TrimSpace(args[0])
	}
	if len(args) > 1 {
		credential = args[1]
	}
	if len(args) > 2 {
		format = strings.TrimSpace(args[2])
	}
	if err := s.ValidateDevice(groupID, name, method, format); err != nil {
		return 0, err
	}
	id, err := s.ReserveDevice(groupID, name, method, format)
	if err != nil {
		return 0, err
	}
	if credential != "" {
		if err := s.SetDeviceCredential(id, credential, 0, ""); err != nil {
			_ = s.DeleteDevice(id)
			return 0, err
		}
	}
	return id, nil
}

// ReserveDevice persists the desired device identity before the agent creates
// any external credential or routing resource. Callers must delete the row if
// provisioning fails.
func (s *Store) ReserveDevice(groupID int64, name, method, format string) (int64, error) {
	name = strings.TrimSpace(name)
	method = strings.TrimSpace(method)
	format = strings.TrimSpace(format)
	if err := s.ValidateDevice(groupID, name, method, format); err != nil {
		return 0, err
	}
	if method == "amneziawg" && format != "native" && format != "app" {
		format = "native"
	}
	r, err := s.DB.Exec(`INSERT INTO devices(group_id,name,method,credential_format,created_at) VALUES(?,?,?,?,?)`, groupID, name, method, format, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) SetDeviceCredential(id int64, credential string, generation int, version string) error {
	if strings.TrimSpace(credential) == "" {
		return errors.New("credential is required")
	}
	if generation < 0 {
		return errors.New("profile generation cannot be negative")
	}
	var method string
	if err := s.DB.QueryRow(`SELECT method FROM devices WHERE id=?`, id).Scan(&method); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("device not found")
		}
		return err
	}
	_, err := s.DB.Exec(`INSERT INTO credentials(device_id,protocol,public_id,secret_blob,profile_generation,protocol_version)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(device_id,protocol) DO UPDATE SET secret_blob=excluded.secret_blob,profile_generation=excluded.profile_generation,protocol_version=excluded.protocol_version`,
		id, method, fmt.Sprintf("%d", id), []byte(credential), generation, strings.TrimSpace(version))
	return err
}

// UpdateDeviceProfiles atomically publishes already validated rendered
// profiles. External resources must be prepared before this transaction and
// compensated by the caller if it fails.
func (s *Store) UpdateDeviceProfiles(updates []DeviceProfileUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, update := range updates {
		name := strings.TrimSpace(update.Name)
		if name == "" || strings.TrimSpace(update.Credential) == "" || update.ProfileGeneration < 0 {
			return errors.New("profile update is incomplete")
		}
		var method string
		if err := tx.QueryRow(`SELECT method FROM devices WHERE id=?`, update.DeviceID).Scan(&method); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("device not found")
			}
			return err
		}
		if _, err := tx.Exec(`UPDATE devices SET name=? WHERE id=?`, name, update.DeviceID); err != nil {
			return err
		}
		result, err := tx.Exec(`UPDATE credentials SET secret_blob=?,profile_generation=?,protocol_version=? WHERE device_id=? AND protocol=?`,
			[]byte(update.Credential), update.ProfileGeneration, strings.TrimSpace(update.ProtocolVersion), update.DeviceID, method)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return errors.New("device credential not found")
		}
	}
	return tx.Commit()
}

func (s *Store) ToggleDevice(id int64, enabled bool) error {
	_, err := s.DB.Exec(`UPDATE devices SET enabled=? WHERE id=?`, boolInt(enabled), id)
	return err
}

func (s *Store) UpdateDevice(id, groupID int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("name is required")
	}
	var exists int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM groups WHERE id=?`, groupID).Scan(&exists); err != nil || exists == 0 {
		return errors.New("group not found")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldGroupID int64
	var method string
	if err := tx.QueryRow(`SELECT group_id,method FROM devices WHERE id=?`, id).Scan(&oldGroupID, &method); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("device not found")
		}
		return err
	}
	r, err := tx.Exec(`UPDATE devices SET name=?,group_id=? WHERE id=?`, name, groupID, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return errors.New("device not found")
	}
	if method == "xray" || method == "xray-xhttp" || method == "amneziawg" {
		month := time.Now().UTC().Format("2006-01")
		for _, affectedGroup := range []int64{oldGroupID, groupID} {
			if err := refreshGroupMethodTraffic(tx, affectedGroup, method, month); err != nil {
				return err
			}
			if err := refreshGroupTraffic(tx, affectedGroup, month); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) Device(id int64) (Device, error) {
	return scanDevice(s.DB.QueryRow(deviceSelect+` WHERE d.id=?`, id))
}

func (s *Store) DeleteDevice(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var groupID int64
	var method string
	if err := tx.QueryRow(`SELECT group_id,method FROM devices WHERE id=?`, id).Scan(&groupID, &method); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("device not found")
		}
		return err
	}
	if _, err := tx.Exec(`DELETE FROM traffic_current WHERE scope_type='device' AND scope_id=?`, id); err != nil {
		return err
	}
	r, err := tx.Exec(`DELETE FROM devices WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return errors.New("device not found")
	}
	if method == "xray" || method == "xray-xhttp" || method == "amneziawg" {
		month := time.Now().UTC().Format("2006-01")
		if err := refreshGroupMethodTraffic(tx, groupID, method, month); err != nil {
			return err
		}
		if err := refreshGroupTraffic(tx, groupID, month); err != nil {
			return err
		}
	}
	return tx.Commit()
}
