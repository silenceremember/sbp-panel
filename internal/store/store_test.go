package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (s *Store) CreateGroup(name string, days int, unlimited ...bool) (int64, error) {
	isUnlimited := len(unlimited) > 0 && unlimited[0]
	return s.CreateGroupWithExpiration(name, "", days, isUnlimited, "")
}

func (s *Store) CreateGroupDetails(name, contact string, days int, unlimited bool) (int64, error) {
	return s.CreateGroupWithExpiration(name, contact, days, unlimited, "")
}

func (s *Store) UpdateGroup(id int64, name, expiresAt string, unlimited bool) error {
	return s.UpdateGroupDetails(id, name, "", expiresAt, unlimited)
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DB.Close() })
	return s
}

func TestOpenReconcilesCurrentCredentialColumnsWithoutVersionChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE devices (id INTEGER PRIMARY KEY, group_id INTEGER NOT NULL, name TEXT NOT NULL, method TEXT NOT NULL DEFAULT 'xray', credential_format TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, last_seen_at TEXT, created_at TEXT NOT NULL);
CREATE TABLE credentials (id INTEGER PRIMARY KEY, device_id INTEGER NOT NULL, protocol TEXT NOT NULL, public_id TEXT NOT NULL, secret_blob BLOB, enabled INTEGER NOT NULL DEFAULT 1, UNIQUE(device_id, protocol));
`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	columns := map[string]bool{}
	rows, err := store.DB.Query(`PRAGMA table_info(credentials)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	rows.Close()
	if !columns["profile_generation"] || !columns["protocol_version"] {
		t.Fatalf("current profile columns were not reconciled: %#v", columns)
	}
}

func TestUpdateDeviceProfilesIsAtomic(t *testing.T) {
	s := testStore(t)
	groupID, err := s.CreateGroup("Family", 30)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := s.CreateDevice(groupID, "First", "xray", "vless://first@example")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDeviceProfiles([]DeviceProfileUpdate{
		{DeviceID: firstID, Name: "Updated", Credential: "vless://updated@example", ProfileGeneration: 1, ProtocolVersion: "26.3.27"},
		{DeviceID: 999999, Name: "Missing", Credential: "vless://missing@example", ProfileGeneration: 1, ProtocolVersion: "26.3.27"},
	}); err == nil {
		t.Fatal("profile transaction unexpectedly succeeded")
	}
	device, err := s.Device(firstID)
	if err != nil {
		t.Fatal(err)
	}
	if device.Name != "First" || device.Credential != "vless://first@example" || device.ProfileGeneration != 0 || device.ProtocolVersion != "" {
		t.Fatalf("failed profile transaction was partially applied: %#v", device)
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("12345678")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "12345678") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(h, "wrong password") {
		t.Fatal("invalid password accepted")
	}
}

func TestChangeOwnerPasswordInvalidatesSessions(t *testing.T) {
	s := testStore(t)
	if err := s.CreateOwner("admin", "old-password"); err != nil {
		t.Fatal(err)
	}
	owner, err := s.Authenticate("admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.CreateSession(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeOwnerPassword("wrong-password", "new-password"); err == nil {
		t.Fatal("incorrect current password accepted")
	}
	if err := s.ChangeOwnerPassword("old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate("admin", "old-password"); err == nil {
		t.Fatal("old password remained valid")
	}
	if _, err := s.Authenticate("admin", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Session(token); err == nil {
		t.Fatal("old session remained valid")
	}
}

func TestCreateSessionPrunesExpiredAndOldRows(t *testing.T) {
	s := testStore(t)
	if err := s.CreateOwner("owner", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	owner, err := s.Authenticate("owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO sessions(token_hash,account_id,csrf,expires_at) VALUES('expired',?,'expired','2000-01-01T00:00:00Z')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	var newest string
	for range maxAccountSessions + 5 {
		newest, _, err = s.CreateSession(owner.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	var count, expired int
	if err := s.DB.QueryRow(`SELECT COUNT(*), SUM(token_hash='expired') FROM sessions WHERE account_id=?`, owner.ID).Scan(&count, &expired); err != nil {
		t.Fatal(err)
	}
	if count != maxAccountSessions || expired != 0 {
		t.Fatalf("sessions after pruning: count=%d expired=%d", count, expired)
	}
	if _, _, err := s.Session(newest); err != nil {
		t.Fatalf("newest session was pruned: %v", err)
	}
}

func TestSQLitePersistentFilesAreBounded(t *testing.T) {
	s := testStore(t)
	for pragma, want := range map[string]int64{
		"journal_size_limit": 4 << 20,
		"wal_autocheckpoint": 256,
		"max_page_count":     65536,
	} {
		var got int64
		if err := s.DB.QueryRow(`PRAGMA ` + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("PRAGMA %s=%d, want %d", pragma, got, want)
		}
	}
}

func TestOwnerSessionGroupsAndDevices(t *testing.T) {
	s := testStore(t)
	if err := s.CreateOwner("owner", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOwner("another", "correct horse battery staple"); err == nil {
		t.Fatal("second owner was accepted")
	}
	a, err := s.Authenticate("owner", "correct horse battery staple")
	if err != nil || a.Role != "owner" {
		t.Fatalf("authentication failed: %#v %v", a, err)
	}
	token, csrf, err := s.CreateSession(a.ID)
	if err != nil || token == "" || csrf == "" {
		t.Fatal("session creation failed")
	}
	got, gotCSRF, err := s.Session(token)
	if err != nil || got.ID != a.ID || gotCSRF != csrf {
		t.Fatal("session lookup failed")
	}
	groupID, err := s.CreateGroup("Family", 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ExtendGroupMonth(groupID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroupDetails(groupID, "Family", "@family-contact", "2030-01-01T00:00:00Z", false); err != nil {
		t.Fatal(err)
	}
	deviceID, err := s.CreateDevice(groupID, "Phone", "xray", "vless://example")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ToggleDevice(deviceID, false); err != nil {
		t.Fatal(err)
	}
	devices, err := s.ListDevices(groupID)
	if err != nil || len(devices) != 1 || devices[0].Enabled || devices[0].Method != "xray" || devices[0].Credential != "vless://example" {
		t.Fatalf("unexpected devices: %#v %v", devices, err)
	}
	awgID, err := s.CreateDevice(groupID, "Amnezia phone", "amneziawg", "[Interface]\nPrivateKey = test", "app")
	if err != nil {
		t.Fatal(err)
	}
	awg, err := s.Device(awgID)
	if err != nil || awg.Format != "app" {
		t.Fatalf("unexpected AWG format: %#v %v", awg, err)
	}
	groupsWithContact, err := s.ListGroups()
	if err != nil || len(groupsWithContact) != 1 || groupsWithContact[0].Contact != "@family-contact" {
		t.Fatalf("unexpected group contact: %#v %v", groupsWithContact, err)
	}
	if err := s.DeleteDevice(awgID); err != nil {
		t.Fatal(err)
	}
	unlimitedID, err := s.CreateGroup("Family unlimited", 30, true)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := s.ListGroups()
	if err != nil || len(groups) != 2 || groups[1].ID != unlimitedID || !groups[1].Unlimited || groups[1].ExpiresAt != nil {
		t.Fatalf("unexpected unlimited group: %#v %v", groups, err)
	}
	if err := s.UpdateGroup(unlimitedID, "Family renamed", "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.ExtendGroupMonth(unlimitedID); err == nil {
		t.Fatal("unlimited group was unexpectedly extended")
	}
	if err := s.UpdateDevice(deviceID, unlimitedID, "Tablet"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(deviceID); err != nil {
		t.Fatal(err)
	}
	devices, err = s.ListDevices(unlimitedID)
	if err != nil || len(devices) != 0 {
		t.Fatalf("device was not deleted: %#v %v", devices, err)
	}
}

func TestMatchingDeviceUsesStableIdentity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "matching-device.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	groupID, err := s.CreateGroup("Family", 30)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateDevice(groupID, "Phone", "xray", "vless://private")
	if err != nil {
		t.Fatal(err)
	}
	device, found, err := s.MatchingDevice(groupID, "  phone  ", "xray", "")
	if err != nil || !found || device.ID != id || device.Credential != "vless://private" {
		t.Fatalf("matching device = %#v, %v, %v", device, found, err)
	}
	if _, found, err := s.MatchingDevice(groupID, "Phone", "amneziawg", "native"); err != nil || found {
		t.Fatalf("different protocol matched: found=%v err=%v", found, err)
	}
}

func TestGroupsAndDevicesAreSortedByName(t *testing.T) {
	s := testStore(t)
	for _, name := range []string{"zeta", "Beta", "alpha"} {
		if _, err := s.CreateGroup(name, 30); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := s.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Name != "alpha" || groups[1].Name != "Beta" || groups[2].Name != "zeta" {
		t.Fatalf("groups are not sorted by name: %#v", groups)
	}

	groupID := groups[0].ID
	for _, name := range []string{"Tablet", "phone", "Android"} {
		if _, err := s.CreateDevice(groupID, name, "xray", "vless://example"); err != nil {
			t.Fatal(err)
		}
	}
	devices, err := s.ListDevices(groupID)
	if err != nil {
		t.Fatal(err)
	}
	if devices[0].Name != "Android" || devices[1].Name != "phone" || devices[2].Name != "Tablet" {
		t.Fatalf("devices are not sorted by name: %#v", devices)
	}
}

func TestEmptyDeviceMetadataListIsNonNil(t *testing.T) {
	s := testStore(t)
	devices, err := s.ListAllDeviceMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if devices == nil || len(devices) != 0 {
		t.Fatalf("empty device list must encode as an array, got %#v", devices)
	}
}

func TestDeviceMetadataOmitsCredentials(t *testing.T) {
	s := testStore(t)
	firstGroup, err := s.CreateGroup("First", 30)
	if err != nil {
		t.Fatal(err)
	}
	secondGroup, err := s.CreateGroup("Second", 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDevice(firstGroup, "Phone", "xray", "vless://secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDevice(secondGroup, "Tablet", "xray", "vless://another-secret"); err != nil {
		t.Fatal(err)
	}

	groupDevices, err := s.ListDeviceMetadata(firstGroup)
	if err != nil {
		t.Fatal(err)
	}
	if len(groupDevices) != 1 || groupDevices[0].Credential != "" {
		t.Fatalf("group metadata exposed a credential: %#v", groupDevices)
	}

	allDevices, err := s.ListAllDeviceMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if len(allDevices) != 2 {
		t.Fatalf("unexpected device metadata: %#v", allDevices)
	}
	for _, device := range allDevices {
		if device.Credential != "" {
			t.Fatalf("all-device metadata exposed a credential: %#v", device)
		}
	}
}

func TestDeleteGroupCascadesDevicesAndTraffic(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	groupID, err := s.CreateGroup("Temporary", 30)
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := s.CreateDevice(groupID, "Phone", "xray", "vless://example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO traffic_current(scope_type,scope_id,protocol,month_key,rx_bytes,tx_bytes) VALUES('group',?,'all','2026-08',1,2),('device',?,'all','2026-08',3,4)`, groupID, deviceID); err != nil {
		t.Fatal(err)
	}
	if count, err := s.CountDevicesByMethod("xray"); err != nil || count != 1 {
		t.Fatalf("unexpected method count=%d err=%v", count, err)
	}
	if err := s.DeleteGroup(groupID); err != nil {
		t.Fatal(err)
	}
	var devices, credentials, traffic int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM devices WHERE group_id=?`, groupID).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM credentials WHERE device_id=?`, deviceID).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM traffic_current WHERE (scope_type='group' AND scope_id=?) OR (scope_type='device' AND scope_id=?)`, groupID, deviceID).Scan(&traffic); err != nil {
		t.Fatal(err)
	}
	if devices != 0 || credentials != 0 || traffic != 0 {
		t.Fatalf("delete left devices=%d credentials=%d traffic=%d", devices, credentials, traffic)
	}
	if count, err := s.CountDevicesByMethod("xray"); err != nil || count != 0 {
		t.Fatalf("method count after delete=%d err=%v", count, err)
	}
}

func TestFindGroupByNameForPublicStatus(t *testing.T) {
	s := testStore(t)
	id, err := s.CreateGroupDetails("Family Plan", "private-contact", 30, false)
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.FindGroupByName("  family plan  ")
	if err != nil {
		t.Fatal(err)
	}
	if group.ID != id || group.Name != "Family Plan" || group.Status != "active" || group.ExpiresAt == nil || group.Unlimited {
		t.Fatalf("unexpected public group lookup: %#v", group)
	}
	if group, err := s.FindGroupByName("family_plan"); err != nil || group.ID != id {
		t.Fatalf("underscored public lookup failed: %#v %v", group, err)
	}
	if _, err := s.CreateGroup("family_plan", 30); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("normalized duplicate group was accepted: %v", err)
	}
	if _, err := s.CreateGroup("Bad/name?", 30); err == nil || !strings.Contains(err.Error(), "only letters") {
		t.Fatalf("unsafe group name was accepted: %v", err)
	}
	if group.Contact != "" || group.RXBytes != 0 || group.TXBytes != 0 {
		t.Fatalf("private fields leaked into lookup: %#v", group)
	}
	if _, err := s.FindGroupByName("missing"); err != sql.ErrNoRows {
		t.Fatalf("missing group returned err=%v", err)
	}
	if _, err := s.CreateGroup("Семья", 30); err != nil {
		t.Fatal(err)
	}
	if group, err := s.FindGroupByName("семья"); err != nil || group.Name != "Семья" {
		t.Fatalf("unicode case-insensitive lookup failed: %#v %v", group, err)
	}
}

func TestGroupAccessStateAndExactExpiration(t *testing.T) {
	s := testStore(t)
	expiration := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Format(time.RFC3339)
	id, err := s.CreateGroupWithExpiration("Timed access", "", 30, false, expiration)
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.Group(id)
	if err != nil {
		t.Fatal(err)
	}
	if group.Status != "expired" || group.ExpiresAt == nil || *group.ExpiresAt != expiration || !group.AccessApplied {
		t.Fatalf("unexpected initial access state: %#v", group)
	}
	if err := s.SetGroupAccessApplied(id, false); err != nil {
		t.Fatal(err)
	}
	group, err = s.Group(id)
	if err != nil {
		t.Fatal(err)
	}
	if group.AccessApplied {
		t.Fatalf("access state was not updated: %#v", group)
	}
	if _, err := s.CreateGroupWithExpiration("Invalid expiration", "", 30, false, "tomorrow"); err == nil {
		t.Fatal("invalid exact expiration was accepted")
	}
}

func TestGroupProtocolTrafficUsesCurrentMonthAndSumsProviders(t *testing.T) {
	s := testStore(t)
	groupID, err := s.CreateGroup("Family", 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupProtocolTraffic(groupID, "bypass-wb", "2026-08", 100, 200); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupProtocolTraffic(groupID, "bypass-vk", "2026-08", 30, 40); err != nil {
		t.Fatal(err)
	}
	groups, err := s.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].RXBytes != 130 || groups[0].TXBytes != 240 {
		t.Fatalf("unexpected group traffic: %#v", groups)
	}
	if err := s.SetGroupProtocolTraffic(groupID, "bypass-wb", "2026-09", 5, 7); err != nil {
		t.Fatal(err)
	}
	var oldRows, rx, tx int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM traffic_current WHERE scope_type='group' AND scope_id=? AND month_key='2026-08'`, groupID).Scan(&oldRows); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT rx_bytes,tx_bytes FROM traffic_current WHERE scope_type='group' AND scope_id=? AND protocol='all'`, groupID).Scan(&rx, &tx); err != nil {
		t.Fatal(err)
	}
	if oldRows != 0 || rx != 5 || tx != 7 {
		t.Fatalf("month rotation failed: old=%d rx=%d tx=%d", oldRows, rx, tx)
	}
}

func TestDeviceTrafficSamplesRollUpToGroup(t *testing.T) {
	s := testStore(t)
	groupID, err := s.CreateGroup("Traffic family", 30)
	if err != nil {
		t.Fatal(err)
	}
	xrayID, err := s.CreateDevice(groupID, "Laptop", "xray", "vless://id@example")
	if err != nil {
		t.Fatal(err)
	}
	awgID, err := s.CreateDevice(groupID, "Phone", "amneziawg", "[Interface]\nPrivateKey = key", "native")
	if err != nil {
		t.Fatal(err)
	}
	month := time.Now().UTC().Format("2006-01")
	if err := s.SetGroupProtocolTraffic(groupID, "bypass-wb", month, 10, 20); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceTrafficSamples(month, []DeviceTrafficSample{
		{DeviceID: xrayID, GroupID: groupID, Protocol: "xray", RXBytes: 100, TXBytes: 50},
		{DeviceID: awgID, GroupID: groupID, Protocol: "amneziawg", RXBytes: 300, TXBytes: 200},
	}); err != nil {
		t.Fatal(err)
	}
	devices, err := s.ListDevices(groupID)
	if err != nil {
		t.Fatal(err)
	}
	traffic := map[int64]int64{}
	for _, device := range devices {
		traffic[device.ID] = device.RXBytes + device.TXBytes
	}
	if traffic[xrayID] != 150 || traffic[awgID] != 500 {
		t.Fatalf("device traffic = %#v", traffic)
	}
	groups, err := s.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].RXBytes+groups[0].TXBytes != 680 {
		t.Fatalf("group traffic = %#v", groups)
	}
	if err := s.DeleteDevice(xrayID); err != nil {
		t.Fatal(err)
	}
	groups, err = s.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].RXBytes+groups[0].TXBytes != 530 {
		t.Fatalf("group traffic after delete = %#v", groups[0])
	}
	if _, err := s.DB.Exec(`INSERT INTO traffic_current(scope_type,scope_id,protocol,month_key,rx_bytes,tx_bytes) VALUES('group',999,'all','2000-01',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceTrafficSamples(month, nil); err != nil {
		t.Fatal(err)
	}
	var oldRows int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM traffic_current WHERE month_key<>?`, month).Scan(&oldRows); err != nil || oldRows != 0 {
		t.Fatalf("old traffic rows = %d, %v", oldRows, err)
	}
}

func TestXrayXHTTPDeviceAndTrafficAreAccepted(t *testing.T) {
	s := testStore(t)
	groupID, err := s.CreateGroup("XHTTP", 30)
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := s.CreateDevice(groupID, "Phone", "xray-xhttp", "vless://device@example:28443?type=xhttp")
	if err != nil {
		t.Fatal(err)
	}
	month := time.Now().UTC().Format("2006-01")
	if err := s.SetDeviceTrafficSamples(month, []DeviceTrafficSample{{DeviceID: deviceID, GroupID: groupID, Protocol: "xray-xhttp", RXBytes: 90, TXBytes: 10}}); err != nil {
		t.Fatal(err)
	}
	devices, err := s.ListDevices(groupID)
	if err != nil || len(devices) != 1 || devices[0].Method != "xray-xhttp" || devices[0].RXBytes != 90 || devices[0].TXBytes != 10 {
		t.Fatalf("XHTTP device = %#v, %v", devices, err)
	}
}
