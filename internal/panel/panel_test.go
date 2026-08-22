package panel

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/silenceremember/sbp-panel/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateChannelForwarding(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		query      string
		wantPath   string
		wantStatus int
	}{
		{"stable check", http.MethodGet, "", "/v1/update", http.StatusOK},
		{"all-release check", http.MethodGet, "include_prereleases=1", "/v1/update?include_prereleases=1", http.StatusOK},
		{"all-release install", http.MethodPost, "include_prereleases=1", "/v1/update?include_prereleases=1", http.StatusOK},
		{"unknown value", http.MethodGet, "include_prereleases=true", "", http.StatusBadRequest},
		{"extra parameter", http.MethodPost, "include_prereleases=1&extra=1", "", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			agent := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if got := request.URL.RequestURI(); got != test.wantPath || request.Method != test.method {
					t.Fatalf("agent request = %s %s", request.Method, got)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
			})}
			s := &server{agent: agent}
			request := httptest.NewRequest(test.method, "/api/update?"+test.query, nil)
			response := httptest.NewRecorder()
			if test.method == http.MethodGet {
				s.updateInfo(response, request)
			} else {
				s.applyUpdate(response, request)
			}
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusOK && calls != 1 || test.wantStatus != http.StatusOK && calls != 0 {
				t.Fatalf("agent calls=%d", calls)
			}
		})
	}
}

func TestAmneziaAppCredentialRoundTrip(t *testing.T) {
	native := "[Interface]\nPrivateKey = secret\n[Peer]\nEndpoint = 192.0.2.1:443\n"
	got := displayCredential(store.Device{Method: "amneziawg", Format: "app", Credential: native})
	if !strings.HasPrefix(got, "vpn://") {
		t.Fatalf("missing vpn scheme: %q", got)
	}
	packed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(got, "vpn://"))
	if err != nil || len(packed) < 5 {
		t.Fatalf("invalid payload: %v", err)
	}
	if size := binary.BigEndian.Uint32(packed[:4]); int(size) != len(native) {
		t.Fatalf("wrong qCompress length: %d", size)
	}
	zr, err := zlib.NewReader(bytes.NewReader(packed[4:]))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(zr)
	_ = zr.Close()
	if err != nil || string(decoded) != native {
		t.Fatalf("round trip failed: %q %v", decoded, err)
	}
}

func TestDeviceTrafficPublicID(t *testing.T) {
	if got, ok := deviceTrafficPublicID(store.Device{Method: "xray", Credential: "vless://device-uuid@example.com:443"}); !ok || got != "device-uuid" {
		t.Fatalf("Xray public ID = %q, %v", got, ok)
	}
	if got, ok := deviceTrafficPublicID(store.Device{Method: "xray-xhttp", Credential: "vless://xhttp-uuid@example.com:28443?type=xhttp"}); !ok || got != "xhttp-uuid" {
		t.Fatalf("Xray XHTTP public ID = %q, %v", got, ok)
	}
	privateBytes := make([]byte, 32)
	for index := range privateBytes {
		privateBytes[index] = byte(index + 1)
	}
	private, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		t.Fatal(err)
	}
	credential := "[Interface]\nAddress = 10.8.1.2/32\nPrivateKey = " + base64.StdEncoding.EncodeToString(privateBytes) + "\n[Peer]\nPublicKey = server\n"
	got, ok := deviceTrafficPublicID(store.Device{Method: "amneziawg", Credential: credential})
	want := base64.StdEncoding.EncodeToString(private.PublicKey().Bytes())
	if !ok || got != want {
		t.Fatalf("AmneziaWG public ID = %q, %v; want %q", got, ok, want)
	}
}

func TestProcessTrafficMetricsKeepsXrayVariantsSeparate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	groupID, err := db.CreateGroupWithExpiration("Traffic", "", 30, false, "")
	if err != nil {
		t.Fatal(err)
	}
	stableID, err := db.CreateDevice(groupID, "Stable", "xray", "vless://same-public-id@example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	xhttpID, err := db.CreateDevice(groupID, "XHTTP", "xray-xhttp", "vless://same-public-id@example.com:28443?type=xhttp")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{db: db}
	body := []byte(fmt.Sprintf(`{"month_key":%q,"device_traffic":[{"protocol":"xray","public_id":"same-public-id","rx_bytes":10,"tx_bytes":1},{"protocol":"xray-xhttp","public_id":"same-public-id","rx_bytes":20,"tx_bytes":2}]}`, time.Now().UTC().Format("2006-01")))
	processed := s.processTrafficMetrics(body)
	var response struct {
		Managed []struct {
			DeviceID int64  `json:"device_id"`
			RXBytes  uint64 `json:"rx_bytes"`
			TXBytes  uint64 `json:"tx_bytes"`
		} `json:"managed_devices"`
	}
	if err := json.Unmarshal(processed, &response); err != nil {
		t.Fatal(err)
	}
	got := map[int64][2]uint64{}
	for _, sample := range response.Managed {
		got[sample.DeviceID] = [2]uint64{sample.RXBytes, sample.TXBytes}
	}
	if got[stableID] != [2]uint64{10, 1} || got[xhttpID] != [2]uint64{20, 2} {
		t.Fatalf("variant traffic attribution = %#v", got)
	}
}

func TestNameBypassRoomsUsesGroupNamesAndKeepsOrphansVisible(t *testing.T) {
	rooms := []bypassRoomSummary{
		{GroupID: 99, Provider: "vk", Code: "orphan"},
		{GroupID: 2, Provider: "wbstream", Code: "second"},
		{GroupID: 1, Provider: "wbstream", Code: "first"},
	}
	groups := []store.Group{{ID: 1, Name: "Alpha"}, {ID: 2, Name: "Beta"}}
	got := nameBypassRooms(rooms, groups)
	if len(got) != 3 || got[0].GroupName != "Alpha" || got[1].GroupName != "Beta" || got[2].GroupName != "Unknown group #99" {
		t.Fatalf("unexpected rooms: %#v", got)
	}
}

func TestProcessTrafficMetricsMapsManagedDevicesWithoutExposingPublicIDs(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	groupID, err := db.CreateGroupWithExpiration("Traffic", "", 30, false, "")
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := db.CreateDevice(groupID, "Laptop", "xray", "vless://device-uuid@example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{db: db}
	body := []byte(fmt.Sprintf(`{"month_key":%q,"device_traffic":[{"protocol":"xray","public_id":"device-uuid","rx_bytes":120,"tx_bytes":30}]}`, time.Now().UTC().Format("2006-01")))
	processed := s.processTrafficMetrics(body)
	if strings.Contains(string(processed), "device-uuid") || strings.Contains(string(processed), "public_id") {
		t.Fatalf("response exposed a public credential identifier: %s", processed)
	}
	var response struct {
		Managed []struct {
			DeviceID int64  `json:"device_id"`
			RXBytes  uint64 `json:"rx_bytes"`
			TXBytes  uint64 `json:"tx_bytes"`
		} `json:"managed_devices"`
	}
	if err := json.Unmarshal(processed, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Managed) != 1 || response.Managed[0].DeviceID != deviceID || response.Managed[0].RXBytes != 120 || response.Managed[0].TXBytes != 30 {
		t.Fatalf("managed traffic = %#v", response.Managed)
	}
	devices, err := db.ListDevices(groupID)
	if err != nil || len(devices) != 1 || devices[0].RXBytes != 120 || devices[0].TXBytes != 30 {
		t.Fatalf("stored device traffic = %#v, %v", devices, err)
	}
}

func TestNativeCredentialUnchanged(t *testing.T) {
	native := "[Interface]\nPrivateKey = secret\n"
	if got := displayCredential(store.Device{Method: "amneziawg", Format: "native", Credential: native}); got != native {
		t.Fatalf("native config changed: %q", got)
	}
}

func TestCredentialIsExcludedFromStateAndFetchedSeparately(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if err := db.CreateOwner("admin", "test-password"); err != nil {
		t.Fatal(err)
	}
	account, err := db.Authenticate("admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.CreateSession(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	groupID, err := db.CreateGroupWithExpiration("Family", "", 30, false, "")
	if err != nil {
		t.Fatal(err)
	}
	const credential = "vless://private-credential"
	deviceID, err := db.CreateDevice(groupID, "Phone", "xray", credential)
	if err != nil {
		t.Fatal(err)
	}

	s := &server{db: db, tries: map[string]attempt{}, checks: map[string]attempt{}}
	mux := http.NewServeMux()
	s.routes(mux)

	request := func(path string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "vpn_session", Value: token})
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}

	stateResponse := request("/api/state", true)
	if stateResponse.Code != http.StatusOK {
		t.Fatalf("state failed: status=%d body=%q", stateResponse.Code, stateResponse.Body.String())
	}
	stateBody := stateResponse.Body.String()
	if !strings.Contains(stateBody, `"devices":[`) || !strings.Contains(stateBody, `"name":"Phone"`) {
		t.Fatalf("state did not include device metadata: %s", stateBody)
	}
	if strings.Contains(stateBody, credential) || strings.Contains(stateBody, `"credential"`) {
		t.Fatalf("state exposed a credential: %s", stateBody)
	}

	devicesResponse := request(fmt.Sprintf("/api/groups/%d/devices", groupID), true)
	if devicesResponse.Code != http.StatusOK || strings.Contains(devicesResponse.Body.String(), `"credential"`) {
		t.Fatalf("device list exposed a credential: status=%d body=%q", devicesResponse.Code, devicesResponse.Body.String())
	}

	credentialPath := fmt.Sprintf("/api/devices/%d/credential", deviceID)
	unauthenticatedResponse := request(credentialPath, false)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("credential endpoint allowed an unauthenticated request: status=%d", unauthenticatedResponse.Code)
	}
	authenticatedResponse := request(credentialPath, true)
	if authenticatedResponse.Code != http.StatusOK || !strings.Contains(authenticatedResponse.Body.String(), credential) {
		t.Fatalf("credential endpoint failed: status=%d body=%q", authenticatedResponse.Code, authenticatedResponse.Body.String())
	}
	if authenticatedResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("credential response is cacheable: %#v", authenticatedResponse.Header())
	}
}

func TestCreateDeviceResponseStillIncludesCredential(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	groupID, err := db.CreateGroupWithExpiration("Family", "", 30, false, "")
	if err != nil {
		t.Fatal(err)
	}
	const credential = "vless://new-private-credential"
	var profileName string
	agent := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		profileName = payload.Name
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"credential":"` + credential + `"}`)),
		}, nil
	})}
	s := &server{db: db, agent: agent}
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/groups/%d/devices", groupID), strings.NewReader(`{"name":"Phone","method":"xray"}`))
	req.SetPathValue("id", fmt.Sprint(groupID))
	response := httptest.NewRecorder()

	s.createDevice(response, req)

	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), credential) {
		t.Fatalf("create response lost its credential: status=%d body=%q", response.Code, response.Body.String())
	}
	if profileName != "SBP · Family · Phone" {
		t.Fatalf("unexpected VPN profile name: %q", profileName)
	}
}

func TestRepeatedCreateDeviceReturnsConflict(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	groupID, err := db.CreateGroupWithExpiration("Family", "", 30, false, "")
	if err != nil {
		t.Fatal(err)
	}
	const credential = "vless://one-private-credential"
	provisionCalls := 0
	agent := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		provisionCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"credential":"` + credential + `"}`)),
		}, nil
	})}
	s := &server{db: db, agent: agent}
	create := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/groups/%d/devices", groupID), strings.NewReader(`{"name":"Phone","method":"xray"}`))
		req.SetPathValue("id", fmt.Sprint(groupID))
		response := httptest.NewRecorder()
		s.createDevice(response, req)
		return response
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- create() }()
	go func() { results <- create() }()
	first, second := <-results, <-results
	statuses := map[int]int{first.Code: 1}
	statuses[second.Code]++
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("create statuses = %d, %d; bodies = %q, %q", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if provisionCalls != 1 {
		t.Fatalf("credential provisioned %d times, want 1", provisionCalls)
	}
	conflictBody := first.Body.String()
	if first.Code == http.StatusCreated {
		conflictBody = second.Body.String()
	}
	if !strings.Contains(conflictBody, `already exists in this group`) {
		t.Fatalf("duplicate response did not explain the conflict: %q", conflictBody)
	}
	devices, err := db.ListDevices(groupID)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices = %#v, err=%v", devices, err)
	}
}

func TestDeleteGroupRevokesAndDeletesEveryDevice(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	groupID, err := db.CreateGroupWithExpiration("Temporary", "", 30, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range []struct{ name, method, port string }{
		{"Phone", "xray", "443"}, {"Laptop", "xray", "443"}, {"Tablet", "xray-xhttp", "28443"},
	} {
		if _, err := db.CreateDevice(groupID, device.name, device.method, "vless://"+strings.ToLower(device.name)+"@example:"+device.port); err != nil {
			t.Fatal(err)
		}
	}
	revocations := map[string]int{}
	agent := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPut && request.URL.Path == "/v1/xray/credentials" {
			var payload struct {
				Method      string
				Credentials []string
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				return nil, err
			}
			revocations[payload.Method] += len(payload.Credentials)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})}
	s := &server{db: db, agent: agent}
	request := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/groups/%d", groupID), nil)
	request.SetPathValue("id", fmt.Sprint(groupID))
	response := httptest.NewRecorder()

	s.deleteGroup(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("delete group status=%d body=%q", response.Code, response.Body.String())
	}
	if revocations["xray"] != 2 || revocations["xray-xhttp"] != 1 {
		t.Fatalf("revocations were not partitioned by Xray runtime: %#v", revocations)
	}
	if _, err := db.Group(groupID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("group still exists: %v", err)
	}
	devices, err := db.ListDevices(groupID)
	if err != nil || len(devices) != 0 {
		t.Fatalf("group devices after deletion = %#v, err=%v", devices, err)
	}
}

func TestNormalizeAmneziaWGProtocolChoice(t *testing.T) {
	tests := []struct {
		method, wantFormat string
	}{
		{"amneziawg-app", "app"},
		{"amneziawg-native", "native"},
	}
	for _, test := range tests {
		method, format := normalizeDeviceMethod(test.method, "")
		if method != "amneziawg" || format != test.wantFormat {
			t.Fatalf("normalizeDeviceMethod(%q)=(%q,%q)", test.method, method, format)
		}
	}
}

func TestUniqueBypassMethods(t *testing.T) {
	devices := []store.Device{{Method: "xray"}, {Method: "bypass-wb"}, {Method: "bypass-wb"}, {Method: "bypass-vk"}}
	methods := uniqueBypassMethods(devices)
	if len(methods) != 2 || methods[0] != "bypass-wb" || methods[1] != "bypass-vk" {
		t.Fatalf("unexpected bypass methods: %#v", methods)
	}
}

func TestRemoveBypassRoomCanPreserveRoomIdentity(t *testing.T) {
	var queries []string
	agent := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		queries = append(queries, request.URL.RawQuery)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	s := &server{agent: agent}
	if err := s.removeBypassRoom(12, "bypass-wb", true); err != nil {
		t.Fatal(err)
	}
	if err := s.removeBypassRoom(12, "bypass-wb", false); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[0] != "preserve=true" || queries[1] != "" {
		t.Fatalf("unexpected room removal queries: %#v", queries)
	}
}

func TestPublicGroupCheckRoutes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if _, err := db.CreateGroupWithExpiration("Семья", "private-contact", 30, false, ""); err != nil {
		t.Fatal(err)
	}
	s := &server{db: db, tries: map[string]attempt{}, checks: map[string]attempt{}}
	mux := http.NewServeMux()
	s.routes(mux)
	handler := securityHeaders(mux)

	pageRequest := httptest.NewRequest(http.MethodGet, "/check", nil)
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK || !strings.Contains(pageResponse.Body.String(), "Check access") {
		t.Fatalf("unexpected check page: status=%d body=%q", pageResponse.Code, pageResponse.Body.String())
	}
	if pageResponse.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("public check page is indexable: %#v", pageResponse.Header())
	}
	directRequest := httptest.NewRequest(http.MethodGet, "/check/group", nil)
	directResponse := httptest.NewRecorder()
	handler.ServeHTTP(directResponse, directRequest)
	if directResponse.Code != http.StatusOK || !strings.Contains(directResponse.Body.String(), "Check access") {
		t.Fatalf("unexpected direct check page: status=%d body=%q", directResponse.Code, directResponse.Body.String())
	}
	if directResponse.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("direct check page is indexable: %#v", directResponse.Header())
	}

	checkRequest := httptest.NewRequest(http.MethodPost, "/api/check-group", strings.NewReader(`{"name":"семья"}`))
	checkRequest.Header.Set("Content-Type", "application/json")
	checkResponse := httptest.NewRecorder()
	handler.ServeHTTP(checkResponse, checkRequest)
	responseBody := checkResponse.Body.String()
	if checkResponse.Code != http.StatusOK || !strings.Contains(responseBody, `"name":"Семья"`) || !strings.Contains(responseBody, `"status":"active"`) {
		t.Fatalf("unexpected group check: status=%d body=%q", checkResponse.Code, responseBody)
	}
	for _, privateField := range []string{"contact", "devices", "credential", "rx_bytes", "tx_bytes"} {
		if strings.Contains(responseBody, privateField) {
			t.Fatalf("public response leaked %q: %s", privateField, responseBody)
		}
	}
}

func TestPasswordVerificationConcurrencyLimit(t *testing.T) {
	s := &server{}
	releaseFirst, ok := s.beginPasswordCheck()
	if !ok {
		t.Fatal("first password verification slot was rejected")
	}
	releaseSecond, ok := s.beginPasswordCheck()
	if !ok {
		t.Fatal("second password verification slot was rejected")
	}
	if releaseThird, ok := s.beginPasswordCheck(); ok {
		releaseThird()
		t.Fatal("third simultaneous password verification was accepted")
	}
	releaseFirst()
	if releaseReplacement, ok := s.beginPasswordCheck(); !ok {
		t.Fatal("released password verification slot was not reusable")
	} else {
		releaseReplacement()
	}
	releaseSecond()
}

func TestBusyPasswordVerificationRejectsLoginBeforeDatabaseWork(t *testing.T) {
	s := &server{tries: map[string]attempt{}, checks: map[string]attempt{}}
	releaseFirst, _ := s.beginPasswordCheck()
	releaseSecond, _ := s.beginPasswordCheck()
	defer releaseFirst()
	defer releaseSecond()

	request := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"Login":"admin","Password":"password"}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	s.login(response, request)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "password verification is busy") {
		t.Fatalf("busy login was not rejected early: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestAttemptTrackingHasHardCapacity(t *testing.T) {
	s := &server{tries: map[string]attempt{}, checks: map[string]attempt{}}
	for i := 0; i < maxTrackedIPEntries+200; i++ {
		request := httptest.NewRequest(http.MethodPost, "/api/login", nil)
		request.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i)
		if i%2 == 0 {
			s.allowAttempt(request)
		} else {
			s.allowCheckAttempt(request)
		}
	}
	if got := len(s.tries) + len(s.checks); got != maxTrackedIPEntries {
		t.Fatalf("attempt tracking exceeded its hard capacity: got %d, want %d", got, maxTrackedIPEntries)
	}
}

func TestDeleteDeviceWithMalformedCredential(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	groupID, err := db.CreateGroupWithExpiration("Broken profiles", "", 30, false, "")
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := db.CreateDevice(groupID, "Broken Xray", "xray", "not-a-vless-url", "")
	if err != nil {
		t.Fatal(err)
	}
	agent := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":"failed to extract the UUID from the VLESS URL"}`)),
		}, nil
	})}
	s := &server{db: db, agent: agent}
	request := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/devices/%d", deviceID), nil)
	request.SetPathValue("id", fmt.Sprint(deviceID))
	request = request.WithContext(context.WithValue(request.Context(), accountKey, store.Account{Role: "admin"}))
	response := httptest.NewRecorder()

	s.deleteDevice(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"warning"`) {
		t.Fatalf("malformed credential was not deleted with a warning: status=%d body=%q", response.Code, response.Body.String())
	}
	if _, err := db.Device(deviceID); err == nil {
		t.Fatal("malformed device still exists after deletion")
	}
}

func TestDeleteDeviceIsIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	groupID, err := db.CreateGroupWithExpiration("Family", "", 30, false, "")
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := db.CreateDevice(groupID, "Phone", "amneziawg", "credential", "app")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteDevice(deviceID); err != nil {
		t.Fatal(err)
	}
	s := &server{db: db}
	request := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/devices/%d", deviceID), nil)
	request.SetPathValue("id", fmt.Sprint(deviceID))
	response := httptest.NewRecorder()

	s.deleteDevice(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("repeated deletion should succeed: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestDiscardableCredentialErrorsAreNarrow(t *testing.T) {
	if !discardableCredentialError(fmt.Errorf("failed to extract the UUID from the VLESS URL")) {
		t.Fatal("malformed VLESS URL was not recognized")
	}
	if discardableCredentialError(fmt.Errorf("agent unavailable: timeout")) {
		t.Fatal("agent outage must not be treated as a discardable credential error")
	}
}

func TestExpiredGroupReconcilerRevokesAccessOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	groupID, err := db.CreateGroupWithExpiration("Expired access", "", 30, false, expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateDevice(groupID, "Phone", "xray", "vless://id@example:443", ""); err != nil {
		t.Fatal(err)
	}
	var states []bool
	agent := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct{ Enabled bool }
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		states = append(states, body.Enabled)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	s := &server{db: db, agent: agent}
	if err := s.reconcileGroupAccess(groupID); err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0] {
		t.Fatalf("expired access was not revoked: %#v", states)
	}
	group, err := db.Group(groupID)
	if err != nil || group.AccessApplied {
		t.Fatalf("revoked state was not persisted: %#v %v", group, err)
	}
	if err := s.reconcileGroupAccess(groupID); err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("idempotent reconciliation repeated external work: %#v", states)
	}
	if err := db.ExtendGroupMonth(groupID); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileGroupAccess(groupID); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || !states[1] {
		t.Fatalf("extended access was not restored: %#v", states)
	}
}
