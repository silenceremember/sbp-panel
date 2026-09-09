package panel

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestXrayNativeProfilePreservesTransportAndIdentity(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		link := "vless://11111111-2222-4333-8444-555555555555@192.0.2.1:28443?type=" + transport + "&path=%2Fprivate%2Fpath&security=reality&sni=example.com&fp=firefox&pbk=server-key&sid=0123456789abcdef#Phone"
		if transport == "tcp" {
			link += ""
			u, _ := url.Parse(link)
			q := u.Query()
			q.Set("flow", "xtls-rprx-vision")
			u.RawQuery = q.Encode()
			link = u.String()
		}
		body, err := xrayNativeProfile(link)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Inbounds  []json.RawMessage
			Outbounds []struct {
				Settings struct {
					Vnext []struct {
						Address string
						Port    int
						Users   []struct{ ID, Encryption, Flow string }
					}
				}
				StreamSettings struct {
					Network         string
					XhttpSettings   struct{ Path, Mode string }
					RealitySettings struct{ ServerName, Fingerprint, PublicKey, ShortID string }
				}
			}
		}
		if err := json.Unmarshal([]byte(body), &config); err != nil {
			t.Fatal(err)
		}
		if len(config.Inbounds) != 1 || len(config.Outbounds) != 1 {
			t.Fatal("Amnezia requires inbounds and outbounds")
		}
		outbound := config.Outbounds[0]
		stream := outbound.StreamSettings
		server := outbound.Settings.Vnext[0]
		if server.Address != "192.0.2.1" || server.Port != 28443 || server.Users[0].ID != "11111111-2222-4333-8444-555555555555" || stream.RealitySettings.Fingerprint != "firefox" || stream.RealitySettings.ShortID != "0123456789abcdef" {
			t.Fatal("identity or REALITY settings lost")
		}
		if transport == "xhttp" && (stream.XhttpSettings.Path != "/private/path" || stream.XhttpSettings.Mode != "auto" || server.Users[0].Flow != "") {
			t.Fatal("XHTTP import must preserve path without Vision flow")
		}
		if transport == "tcp" && server.Users[0].Flow != "xtls-rprx-vision" {
			t.Fatal("TCP lost Vision")
		}
	}
}

func TestFingerprintChangePreservesCredentialParameters(t *testing.T) {
	original := "vless://id@example.com:28443?type=xhttp&path=%2Fprivate&pbk=key&sid=short&fp=chrome#Phone"
	changed, err := withXrayFingerprint(original, "firefox")
	if err != nil {
		t.Fatal(err)
	}
	old, _ := url.Parse(original)
	next, _ := url.Parse(changed)
	if next.Query().Get("fp") != "firefox" || next.User.Username() != old.User.Username() || next.Query().Get("path") != old.Query().Get("path") || next.Fragment != old.Fragment {
		t.Fatal("fingerprint changed server identity")
	}
	if _, err := withXrayFingerprint(original, "invalid"); err == nil {
		t.Fatal("invalid fingerprint accepted")
	}
}

func TestPortableConfigurationRejectsInvalidReplacement(t *testing.T) {
	valid := portableConfiguration{Format: "sbp-configuration", Components: []string{"docker", "xray"}, Groups: []portableGroup{{Name: "Family", Unlimited: true, Devices: []portableDevice{{Name: "Phone", Method: "xray", Enabled: false, Fingerprint: "firefox"}}}}}
	if err := validatePortableConfiguration(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Components = []string{"docker"}
	if err := validatePortableConfiguration(invalid); err == nil {
		t.Fatal("accepted a device without its component")
	}
	invalid = valid
	invalid.Groups = append(append([]portableGroup{}, valid.Groups...), valid.Groups[0])
	if err := validatePortableConfiguration(invalid); err == nil {
		t.Fatal("accepted duplicate groups before replacement")
	}
}
