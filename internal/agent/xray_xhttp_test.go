package agent

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewXrayXHTTPConfigIsIsolatedAndHardened(t *testing.T) {
	limit := map[string]any{"afterBytes": 8 * 1024 * 1024, "bytesPerSec": 1024 * 1024, "burstBytesPerSec": 4 * 1024 * 1024}
	root := newXrayConfigFor(xhttpXrayVariant, "private", "0123456789abcdef", "/secret-path", limit)
	inbound, tag, err := managedXrayInbound(root)
	if err != nil {
		t.Fatal(err)
	}
	if tag != xhttpXrayVariant.InboundTag {
		t.Fatalf("inbound tag = %q", tag)
	}
	settings := inbound["settings"].(map[string]any)
	if settings["decryption"] != "none" || len(settings["clients"].([]any)) != 0 {
		t.Fatalf("VLESS settings = %#v", settings)
	}
	stream := inbound["streamSettings"].(map[string]any)
	if stream["network"] != "xhttp" || stream["security"] != "reality" {
		t.Fatalf("stream settings = %#v", stream)
	}
	xhttp := stream["xhttpSettings"].(map[string]any)
	if xhttp["path"] != "/secret-path" {
		t.Fatalf("XHTTP settings = %#v", xhttp)
	}
	reality := stream["realitySettings"].(map[string]any)
	if reality["target"] != "www.cloudflare.com:443" || reality["dest"] != nil {
		t.Fatalf("REALITY target = %#v", reality)
	}
	if reality["limitFallbackUpload"] == nil || reality["limitFallbackDownload"] == nil {
		t.Fatalf("fallback limits missing: %#v", reality)
	}
	if endpoint := xrayAPIEndpoint(root); endpoint != defaultXrayStatsEndpoint {
		t.Fatalf("API endpoint = %q", endpoint)
	}
}

func TestXrayXHTTPCredentialLink(t *testing.T) {
	link, err := xrayCredentialLink(xhttpXrayVariant, "11111111-2222-4333-8444-555555555555", "Phone #1", xrayClientMetadata{
		Server: "198.51.100.7", PublicKey: "public-key", ShortID: "0123456789abcdef",
		SNI: "www.cloudflare.com", Path: "/secret_path-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "vless" || parsed.User.Username() != "11111111-2222-4333-8444-555555555555" || parsed.Host != "198.51.100.7:28443" {
		t.Fatalf("link authority = %q", link)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"encryption": "none", "security": "reality", "sni": "www.cloudflare.com",
		"fp": "chrome", "pbk": "public-key", "sid": "0123456789abcdef",
		"type": "xhttp", "path": "/secret_path-value",
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q; link=%s", key, got, want, link)
		}
	}
	if _, present := query["flow"]; present {
		t.Fatalf("XHTTP link must not contain Vision flow: %s", link)
	}
	if parsed.Fragment != "Phone #1" || !strings.Contains(link, "path=%2Fsecret_path-value") {
		t.Fatalf("link escaping = %q", link)
	}
}

func TestXrayVariantsUseIndependentRuntimeAndTrafficIdentity(t *testing.T) {
	stable := stableXrayVariant.runtimeUser("device")
	xhttp := xhttpXrayVariant.runtimeUser("device")
	if stable.Flow != "xtls-rprx-vision" || xhttp.Flow != "" {
		t.Fatalf("runtime flows: stable=%q xhttp=%q", stable.Flow, xhttp.Flow)
	}
	output := `{"stat":[{"name":"user>>>phone@local>>>traffic>>>downlink","value":"120"}]}`
	got := parseXrayStatsForProtocol(output, map[string]string{"phone@local": "device"}, xhttpXrayVariant.TrafficProtocol)
	if counters := got["xray-xhttp:device"]; counters != [2]uint64{120, 0} {
		t.Fatalf("XHTTP counters = %#v", counters)
	}
	if _, collided := got["xray:device"]; collided {
		t.Fatal("XHTTP counters collided with the stable Xray namespace")
	}
}

func TestKnownXrayVariantsCanCoexist(t *testing.T) {
	containers := "xray-stable\nxray-xhttp\namnezia-awg2"
	if present, err := existingXrayVariantStatus(stableXrayVariant, containers, "/managed/stable.json"); err != nil || !present {
		t.Fatalf("stable presence = %v, %v", present, err)
	}
	if present, err := existingXrayVariantStatus(xhttpXrayVariant, containers, "/managed/xhttp.json"); err != nil || !present {
		t.Fatalf("XHTTP presence = %v, %v", present, err)
	}
	if _, err := existingXrayVariantStatus(xhttpXrayVariant, containers+"\nforeign-xray", ""); err == nil || !strings.Contains(err.Error(), "supported SBP paths") {
		t.Fatalf("same-name container without config must fail first: %v", err)
	}
}

func TestVerifyManagedXrayContainerRequiresExactOwnershipEvidence(t *testing.T) {
	original := dockerCommand
	t.Cleanup(func() { dockerCommand = original })
	pids := int64(128)
	inspect := []map[string]any{{
		"Config": map[string]any{"Image": xrayImage},
		"HostConfig": map[string]any{
			"Binds":          []string{xhttpXrayVariant.Dir + "/config.json:/etc/xray/config.json:ro"},
			"ReadonlyRootfs": true, "SecurityOpt": []string{"no-new-privileges:true"},
			"CapDrop": []string{"ALL"}, "Memory": int64(256 * 1024 * 1024), "PidsLimit": pids,
			"PortBindings": map[string]any{"8443/tcp": []map[string]string{{"HostIp": "", "HostPort": "28443"}}},
			"LogConfig":    map[string]any{"Type": "none"},
		},
	}}
	body, err := json.Marshal(inspect)
	if err != nil {
		t.Fatal(err)
	}
	dockerCommand = func(args ...string) (string, error) { return string(body), nil }
	if err := verifyManagedXrayContainer(xhttpXrayVariant); err != nil {
		t.Fatalf("valid ownership evidence rejected: %v", err)
	}
	inspect[0]["Config"].(map[string]any)["Image"] = "foreign/image:latest"
	body, _ = json.Marshal(inspect)
	if err := verifyManagedXrayContainer(xhttpXrayVariant); err == nil || !strings.Contains(err.Error(), "refusing mutation") {
		t.Fatalf("foreign image accepted: %v", err)
	}
}

func TestInterruptedXrayXHTTPCleanupValidatesExactMarker(t *testing.T) {
	original := dockerCommand
	t.Cleanup(func() { dockerCommand = original })
	variant := xhttpXrayVariant
	variant.Dir = filepath.Join(t.TempDir(), "xray-xhttp")
	variant.ConfigFile = filepath.Join(variant.Dir, "config.json")
	if err := writeInstallMarker(variant.Dir, installMarker{Containers: []string{"foreign-container"}}); err != nil {
		t.Fatal(err)
	}
	if err := cleanupInterruptedXrayInstall(variant); err == nil || !strings.Contains(err.Error(), "unexpected Docker resources") {
		t.Fatalf("unexpected interrupted marker was accepted: %v", err)
	}
	if _, err := os.Stat(variant.Dir); err != nil {
		t.Fatalf("refused cleanup removed the managed directory: %v", err)
	}
	if err := os.RemoveAll(variant.Dir); err != nil {
		t.Fatal(err)
	}
	if err := writeInstallMarker(variant.Dir, installMarker{Containers: []string{variant.Container}}); err != nil {
		t.Fatal(err)
	}
	dockerCommand = func(args ...string) (string, error) {
		if len(args) > 0 && args[0] == "ps" {
			return "", nil
		}
		return "", nil
	}
	if err := cleanupInterruptedXrayInstall(variant); err != nil {
		t.Fatalf("valid interrupted marker cleanup failed: %v", err)
	}
	if _, err := os.Stat(variant.Dir); !os.IsNotExist(err) {
		t.Fatalf("valid interrupted install directory still exists: %v", err)
	}
}

func TestGeneratedXrayXHTTPConfigWithPinnedBinary(t *testing.T) {
	binary := os.Getenv("SBP_XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("set SBP_XRAY_TEST_BINARY to the pinned Xray 26.3.27 executable")
	}
	privateBytes := make([]byte, 32)
	for index := range privateBytes {
		privateBytes[index] = byte(index + 1)
	}
	private, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		t.Fatal(err)
	}
	root := newXrayConfigFor(xhttpXrayVariant, base64.RawURLEncoding.EncodeToString(private.Bytes()), "0123456789abcdef", "/pinned-binary-test", map[string]any{
		"afterBytes": 8388608, "bytesPerSec": 1048576, "burstBytesPerSec": 4194304,
	})
	body, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "run", "-test", "-config", configPath).CombinedOutput(); err != nil {
		t.Fatalf("pinned Xray rejected generated XHTTP config: %v\n%s", err, output)
	}
}
