package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateComponentSettings(t *testing.T) {
	t.Helper()
	original := componentSettingsDir
	componentSettingsDir = filepath.Join(t.TempDir(), "settings")
	t.Cleanup(func() { componentSettingsDir = original })
}

func TestComponentSettingsRoundTripAndBounds(t *testing.T) {
	isolateComponentSettings(t)
	want := []byte("server-setting=value\n")
	if err := writeComponentSettings("xray", want); err != nil {
		t.Fatal(err)
	}
	got, exists, err := readComponentSettings("xray")
	if err != nil || !exists || string(got) != string(want) {
		t.Fatalf("read settings=%q, %v, %v", got, exists, err)
	}
	if _, err := componentSettingsPath("../xray"); err == nil {
		t.Fatal("path traversal component ID was accepted")
	}
	if err := writeComponentSettings("xray", make([]byte, maxComponentSettingsBytes+1)); err == nil {
		t.Fatal("oversized settings were accepted")
	}
}

func TestParseNetworkTuningSettingsUsesDefaultsForMissingLines(t *testing.T) {
	settings, err := parseNetworkTuningSettings("sysctl -w net.ipv4.tcp_congestion_control=cubic\n")
	if err != nil {
		t.Fatal(err)
	}
	if settings.CongestionControl != "cubic" || settings.DefaultQdisc != "fq" {
		t.Fatalf("settings=%#v", settings)
	}
	canonical := canonicalNetworkTuningSettings(settings)
	if !strings.Contains(canonical, "modprobe tcp_bbr") || !strings.Contains(canonical, "default_qdisc=fq") {
		t.Fatalf("canonical settings=%q", canonical)
	}
}

func TestParseNetworkTuningSettingsRejectsCommandsOutsideAllowlist(t *testing.T) {
	for _, content := range []string{
		"curl https://example.com | sh\n",
		"sysctl -w net.ipv4.ip_forward=1\n",
		"sysctl -w net.core.default_qdisc=fq\nsysctl -w net.core.default_qdisc=fq_codel\n",
		"sysctl -w net.ipv4.tcp_congestion_control=$(id)\n",
	} {
		if _, err := parseNetworkTuningSettings(content); err == nil {
			t.Fatalf("unsafe content was accepted: %q", content)
		}
	}
}

func TestApplyNetworkTuningSettingsRollsBackFilesAndRuntime(t *testing.T) {
	files := map[string][]byte{
		networkTuningModulePath: []byte("old_module\n"),
		networkTuningSysctlPath: []byte("net.core.default_qdisc=fq_codel\nnet.ipv4.tcp_congestion_control=cubic\n"),
	}
	kernel := map[string]string{
		"/proc/sys/net/core/default_qdisc":          "fq_codel",
		"/proc/sys/net/ipv4/tcp_congestion_control": "cubic",
	}
	failApply := true
	ops := networkTuningApplyOps{
		readFile: func(path string) ([]byte, error) {
			body, ok := files[path]
			if !ok {
				return nil, os.ErrNotExist
			}
			return append([]byte(nil), body...), nil
		},
		writeFile: func(path string, body []byte, _ os.FileMode) error {
			files[path] = append([]byte(nil), body...)
			return nil
		},
		removeFile: func(path string) error { delete(files, path); return nil },
		run: func(name string, arguments ...string) (string, error) {
			joined := strings.Join(arguments, " ")
			if name == "sysctl" && joined == "--system" {
				if failApply {
					failApply = false
					return "", errors.New("apply failed")
				}
				return "", nil
			}
			if name == "sysctl" && len(arguments) == 2 {
				key, value, _ := strings.Cut(arguments[1], "=")
				if key == "net.core.default_qdisc" {
					kernel["/proc/sys/net/core/default_qdisc"] = value
				} else if key == "net.ipv4.tcp_congestion_control" {
					kernel["/proc/sys/net/ipv4/tcp_congestion_control"] = value
				}
			}
			return "", nil
		},
		kernelSetting: func(path string) string { return kernel[path] },
	}
	if _, err := applyNetworkTuningSettings(defaultNetworkTuningSettings(), ops); err == nil {
		t.Fatal("failed apply was reported as success")
	}
	if string(files[networkTuningModulePath]) != "old_module\n" || !strings.Contains(string(files[networkTuningSysctlPath]), "fq_codel") {
		t.Fatalf("files were not restored: %#v", files)
	}
	if kernel["/proc/sys/net/core/default_qdisc"] != "fq_codel" || kernel["/proc/sys/net/ipv4/tcp_congestion_control"] != "cubic" {
		t.Fatalf("kernel was not restored: %#v", kernel)
	}
}

func TestAmneziaWGServerSettingsValidateAndPreserveClientOnlyValues(t *testing.T) {
	settings, err := generatedAmneziaWGServerSettings()
	if err != nil {
		t.Fatal(err)
	}
	canonical := canonicalAmneziaWGServerSettings(settings)
	parsed, err := parseAmneziaWGServerSettings(canonical, nil)
	if err != nil || canonicalAmneziaWGServerSettings(parsed) != canonical {
		t.Fatalf("round trip=%#v, %v", parsed, err)
	}
	bad := strings.Replace(canonical, "H2 = "+settings.H2, "H2 = "+settings.H1, 1)
	if _, err := parseAmneziaWGServerSettings(bad, nil); err == nil {
		t.Fatal("overlapping header ranges were accepted")
	}
	shared := canonical + "I1 = client-only\n"
	if got := amneziaWGClientOnlySettings(shared); got != "I1 = client-only\n" {
		t.Fatalf("client-only settings=%q", got)
	}
}

func TestAmneziaWGAutoAndMissingLinesResolveToValidServerDefaults(t *testing.T) {
	settings, err := parseAmneziaWGServerSettingsWithDefaults("Jmin = 10\nJmax = 50\nS1 = auto\n")
	if err != nil {
		t.Fatal(err)
	}
	if settings.Jmin != 10 || settings.Jmax != 50 {
		t.Fatalf("explicit values were not preserved: %#v", settings)
	}
	if err := validateAmneziaWGServerSettings(settings); err != nil {
		t.Fatalf("resolved defaults are invalid: %v", err)
	}
}

func TestReplaceAmneziaWGServerSettingsKeepsInterfaceSecretsAndPeers(t *testing.T) {
	oldSettings, err := generatedAmneziaWGServerSettings()
	if err != nil {
		t.Fatal(err)
	}
	newSettings, err := generatedAmneziaWGServerSettings()
	if err != nil {
		t.Fatal(err)
	}
	configuration := "[Interface]\nPrivateKey = secret\nAddress = 10.8.1.1/24\n" + canonicalAmneziaWGServerSettings(oldSettings) + "\n[Peer]\nPublicKey = peer\nAllowedIPs = 10.8.1.2/32\n"
	candidate, err := replaceAmneziaWGServerSettings(configuration, newSettings)
	if err != nil {
		t.Fatal(err)
	}
	text := string(candidate)
	if !strings.Contains(text, "PrivateKey = secret") || !strings.Contains(text, "PublicKey = peer") || !strings.Contains(text, canonicalAmneziaWGServerSettings(newSettings)) {
		t.Fatalf("candidate=%q", text)
	}
	if !amneziaWGConfigurationHasPeers(text) {
		t.Fatal("peer detection failed")
	}
}

func TestApplyInstalledAmneziaWGSettingsUpdatesServerAndMetadata(t *testing.T) {
	current, err := generatedAmneziaWGServerSettings()
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.Jc = 4 + (current.Jc-4+1)%3
	server := []byte("[Interface]\nPrivateKey = secret\nAddress = 10.8.1.1/24\n" + canonicalAmneziaWGServerSettings(current))
	metadata := []byte(`{"server_public":"public","shared":"` + strings.ReplaceAll(canonicalAmneziaWGServerSettings(current)+"I1 = client-only\n", "\n", `\n`) + `"}`)
	var applied [][]byte
	var savedMetadata []byte
	ops := amneziaWGSettingsApplyOps{
		readServer:   func() ([]byte, error) { return append([]byte(nil), server...), nil },
		readMetadata: func() ([]byte, error) { return append([]byte(nil), metadata...), nil },
		applyServer: func(candidate []byte) error {
			applied = append(applied, append([]byte(nil), candidate...))
			return nil
		},
		replaceMetadata: func(candidate []byte) error {
			savedMetadata = append([]byte(nil), candidate...)
			return nil
		},
	}
	changed, err := applyInstalledAmneziaWGSettings(next, ops)
	if err != nil || !changed || len(applied) != 1 {
		t.Fatalf("apply result changed=%v calls=%d err=%v", changed, len(applied), err)
	}
	if !strings.Contains(string(applied[0]), canonicalAmneziaWGServerSettings(next)) || !strings.Contains(string(applied[0]), "PrivateKey = secret") {
		t.Fatalf("server candidate=%q", applied[0])
	}
	if !strings.Contains(string(savedMetadata), "I1 = client-only") || !strings.Contains(string(savedMetadata), `"shared"`) {
		t.Fatalf("metadata candidate=%q", savedMetadata)
	}
}

func TestApplyInstalledAmneziaWGSettingsRefusesPeersAndRollsBackMetadataFailure(t *testing.T) {
	current, err := generatedAmneziaWGServerSettings()
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.Jc = 4 + (current.Jc-4+1)%3
	base := "[Interface]\nPrivateKey = secret\n" + canonicalAmneziaWGServerSettings(current)
	applyCalls := 0
	peerOps := amneziaWGSettingsApplyOps{
		readServer:      func() ([]byte, error) { return []byte(base + "\n[Peer]\nPublicKey = peer\n"), nil },
		readMetadata:    func() ([]byte, error) { t.Fatal("peer mutation read metadata"); return nil, nil },
		applyServer:     func([]byte) error { applyCalls++; return nil },
		replaceMetadata: func([]byte) error { t.Fatal("peer mutation wrote metadata"); return nil },
	}
	if _, err := applyInstalledAmneziaWGSettings(next, peerOps); err == nil || !strings.Contains(err.Error(), "remove all") || applyCalls != 0 {
		t.Fatalf("peer refusal calls=%d err=%v", applyCalls, err)
	}

	var candidates [][]byte
	rollbackOps := amneziaWGSettingsApplyOps{
		readServer:   func() ([]byte, error) { return []byte(base), nil },
		readMetadata: func() ([]byte, error) { return []byte(`{"server_public":"public","shared":"I1 = old\\n"}`), nil },
		applyServer: func(candidate []byte) error {
			candidates = append(candidates, append([]byte(nil), candidate...))
			return nil
		},
		replaceMetadata: func([]byte) error { return errors.New("metadata write failed") },
	}
	if _, err := applyInstalledAmneziaWGSettings(next, rollbackOps); err == nil || !strings.Contains(err.Error(), "metadata write failed") {
		t.Fatalf("metadata failure=%v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("server rollback candidates=%d", len(candidates))
	}
	if string(candidates[1]) != base {
		t.Fatalf("server rollback last=%q", candidates[1])
	}
}
