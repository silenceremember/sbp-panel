package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/silenceremember/sbp-panel/internal/store"
)

func TestProtocolSettingsRefreshProfilesAndRollbackFailures(t *testing.T) {
	for _, method := range []string{"amneziawg", "xray", "xray-xhttp"} {
		for _, failure := range []string{"", "render", "database"} {
			t.Run(method+"/"+failure, func(t *testing.T) {
				db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.DB.Close()
				groupID, _ := db.CreateGroupWithExpiration("Admin", "", 0, false, "2020-01-01T00:00:00Z")
				first, _ := db.CreateDevice(groupID, "Custom phone", method, "first-old")
				second, _ := db.CreateDevice(groupID, "Second", method, "second-old")
				_ = db.ToggleDevice(second, false)
				if failure == "database" {
					if _, err := db.DB.Exec(`CREATE TRIGGER refuse_publish BEFORE UPDATE ON credentials BEGIN SELECT RAISE(ABORT,'database failure'); END`); err != nil {
						t.Fatal(err)
					}
				}
				settings := "old-settings"
				generation, version := 1, "26.3.27"
				if method == "amneziawg" {
					generation, version = 4, "3.1"
				}
				s := &server{db: db, agent: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					result := map[string]any{"ok": true}
					status := 200
					if strings.HasSuffix(r.URL.Path, "/settings") {
						if r.Method == http.MethodPut {
							var input struct{ Content string }
							_ = json.NewDecoder(r.Body).Decode(&input)
							settings = input.Content
						}
						result["settings"] = map[string]string{"content": settings}
					} else if r.URL.Path == "/v1/credentials/render" {
						var input struct{ Name, Credential string }
						_ = json.NewDecoder(r.Body).Decode(&input)
						if failure == "render" && input.Credential == "second-old" {
							status = 400
							result = map[string]any{"error": "render failed"}
						} else {
							result["credential"], result["profile_generation"], result["protocol_version"] = input.Credential+"-refreshed", generation, version
						}
					} else {
						t.Fatalf("unexpected runtime action: %s", r.URL.Path)
					}
					body, _ := json.Marshal(result)
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				})}}
				response := httptest.NewRecorder()
				s.saveProtocolSettings(response, method, []byte(`{"content":"new-settings"}`))
				wantSettings, wantCredential, wantStatus := "new-settings", "first-old-refreshed", 200
				if failure != "" {
					wantSettings, wantCredential, wantStatus = "old-settings", "first-old", 502
				}
				device, _ := db.Device(first)
				disabled, _ := db.Device(second)
				group, _ := db.Group(groupID)
				if response.Code != wantStatus || settings != wantSettings || device.Credential != wantCredential {
					t.Fatalf("status=%d settings=%s credential=%s body=%s", response.Code, settings, device.Credential, response.Body.String())
				}
				if device.Name != "Custom phone" || disabled.Enabled || group.ExpiresAt == nil || *group.ExpiresAt != "2020-01-01T00:00:00Z" {
					t.Fatal("profile refresh changed names, access or expiration")
				}
			})
		}
	}
}

func TestServerCountryOverrideAndInvalidSettingsAreAtomic(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	s := &server{db: db}
	response := httptest.NewRecorder()
	s.updateServerURL(response, httptest.NewRequest(http.MethodPut, "/api/settings/server-url", strings.NewReader(`{"URL":"https://example.com/server","Country":"Ireland"}`)))
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	s.detectServerCountry() // A manual value must bypass network detection.
	country, _ := db.Setting("server_country")
	if country != "Ireland" {
		t.Fatal("country override lost")
	}
	response = httptest.NewRecorder()
	s.updateServerURL(response, httptest.NewRequest(http.MethodPut, "/api/settings/server-url", strings.NewReader(`{"URL":"https://example.com/changed","Country":"bad\ncountry"}`)))
	serverURL, _ := db.Setting("server_url")
	if response.Code != 400 || serverURL != "https://example.com/server" {
		t.Fatal("invalid country partially saved server settings")
	}
}
