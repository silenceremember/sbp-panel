package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func testXraySNIVariant(t *testing.T, base xrayVariant) (xrayVariant, []byte) {
	t.Helper()
	isolateComponentSettings(t)
	variant := base
	variant.Dir = filepath.Join(t.TempDir(), base.Method)
	variant.ConfigFile = filepath.Join(variant.Dir, "config.json")
	variant.MetadataFile = filepath.Join(variant.Dir, "server.json")
	if err := os.MkdirAll(variant.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	root := newXrayConfigFor(variant, "private", "0123456789abcdef", "/path", nil)
	inbound, _, err := managedXrayInbound(root)
	if err != nil {
		t.Fatal(err)
	}
	settings := inbound["settings"].(map[string]any)
	settings["clients"] = []any{map[string]any{
		"id": "11111111-2222-4333-8444-555555555555", "flow": variant.Flow,
		"email": xrayStatsEmail("11111111-2222-4333-8444-555555555555"), "level": 0,
	}}
	configBody, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(variant.ConfigFile, configBody, 0644); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(xrayClientMetadata{
		Server: "198.51.100.7", PublicKey: "public", ShortID: "0123456789abcdef",
		SNI: xrayRealityServerName, Path: "/path",
	})
	if err := os.WriteFile(variant.MetadataFile, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	return variant, configBody
}

func testXraySNIOps(validate func(string) error, restart func(xrayVariant, map[string]any) error) xrayRealitySNIOps {
	return xrayRealitySNIOps{
		owned:            func(string) bool { return true },
		verifyContainer:  func(xrayVariant) error { return nil },
		validateConfig:   validate,
		validateTarget:   func(string) error { return nil },
		restartAndVerify: restart,
		captureTraffic:   func() {},
	}
}

func TestNormalizeXrayRealityTarget(t *testing.T) {
	for input, want := range map[string]string{" DL.Google.COM:443 ": "dl.google.com:443", "xn--e1afmkfd.xn--p1ai:8443": "xn--e1afmkfd.xn--p1ai:8443"} {
		got, err := normalizeXrayRealityTarget(input)
		if err != nil || got != want {
			t.Fatalf("normalize target %q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"example.com", "https://example.com:443", "example.com:0", "example.com:65536", "127.0.0.1:443", "localhost:443"} {
		if _, err := normalizeXrayRealityTarget(input); err == nil {
			t.Fatalf("invalid target %q was accepted", input)
		}
	}
}

func TestNormalizeXrayRealitySNI(t *testing.T) {
	for input, want := range map[string]string{
		" DL.Google.COM. ":      "dl.google.com",
		"a-b.example":           "a-b.example",
		"xn--e1afmkfd.xn--p1ai": "xn--e1afmkfd.xn--p1ai",
	} {
		got, err := normalizeXrayRealitySNI(input)
		if err != nil || got != want {
			t.Fatalf("normalize %q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"localhost", "https://example.com", "*.example.com", "-a.example", "a_.example", "пример.рф"} {
		if _, err := normalizeXrayRealitySNI(input); err == nil {
			t.Fatalf("invalid SNI %q was accepted", input)
		}
	}
}

func TestReplaceXrayRealitySettingsAppliesTargetAndListOnce(t *testing.T) {
	variant, _ := testXraySNIVariant(t, xhttpXrayVariant)
	validatedTarget := ""
	validatedConfigs := 0
	restarts := 0
	ops := testXraySNIOps(func(string) error { validatedConfigs++; return nil }, func(xrayVariant, map[string]any) error { restarts++; return nil })
	ops.validateTarget = func(target string) error { validatedTarget = target; return nil }

	state, err := mutateXrayRealitySettings(variant, "DL.Google.COM:8443", []string{"dl.google.com", xrayRealityServerName}, ops, "chrome")
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"dl.google.com", xrayRealityServerName}
	if state.Target != "dl.google.com:8443" || !reflect.DeepEqual(state.ServerNames, wantNames) {
		t.Fatalf("replacement state=%#v", state)
	}
	if validatedTarget != state.Target || validatedConfigs != 1 || restarts != 1 {
		t.Fatalf("target=%q config validations=%d restarts=%d", validatedTarget, validatedConfigs, restarts)
	}
	_, _, stored, err := readXrayRealitySNIState(variant)
	if err != nil || !reflect.DeepEqual(stored, state) {
		t.Fatalf("stored replacement=%#v err=%v", stored, err)
	}
}

func TestReplaceXrayRealitySettingsAllowsNewDefault(t *testing.T) {
	variant, _ := testXraySNIVariant(t, stableXrayVariant)
	ops := testXraySNIOps(func(string) error { return nil }, func(xrayVariant, map[string]any) error { return nil })
	state, err := mutateXrayRealitySettings(variant, "dl.google.com:443", []string{"dl.google.com"}, ops, "chrome")
	if err != nil || state.DefaultSNI != "dl.google.com" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	metadata, err := variant.loadClientMetadata()
	if err != nil || metadata.SNI != "dl.google.com" {
		t.Fatalf("new links would use stale SNI: %#v %v", metadata, err)
	}
}

func TestReplaceXrayRealitySettingsSavesDesiredStateBeforeInstall(t *testing.T) {
	variant, before := testXraySNIVariant(t, stableXrayVariant)
	originalSettingsDir := componentSettingsDir
	componentSettingsDir = filepath.Join(t.TempDir(), "settings")
	t.Cleanup(func() { componentSettingsDir = originalSettingsDir })
	ops := testXraySNIOps(func(string) error { return nil }, func(xrayVariant, map[string]any) error { return nil })
	ops.owned = func(string) bool { return false }
	ops.verifyContainer = func(xrayVariant) error { t.Fatal("unowned container was inspected for replacement"); return nil }

	want, err := mutateXrayRealitySettings(variant, "dl.google.com:8443", []string{xrayRealityServerName, "dl.google.com"}, ops, "chrome")
	if err != nil {
		t.Fatal(err)
	}
	saved, _, err := loadDesiredXrayRealitySNIState(variant.Method)
	if err != nil || !reflect.DeepEqual(saved, want) {
		t.Fatalf("saved desired replacement=%#v want=%#v err=%v", saved, want, err)
	}
	after, _ := os.ReadFile(variant.ConfigFile)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("unowned replacement changed the managed configuration")
	}
}

func TestApplyDesiredXrayRealitySNIProjectsSavedListIntoNewConfig(t *testing.T) {
	originalSettingsDir := componentSettingsDir
	componentSettingsDir = filepath.Join(t.TempDir(), "settings")
	t.Cleanup(func() { componentSettingsDir = originalSettingsDir })
	want := xrayRealitySNIState{DefaultSNI: xrayRealityServerName, ServerNames: []string{xrayRealityServerName, "dl.google.com"}, Target: "dl.google.com:443"}
	if err := saveDesiredXrayRealitySNIState(stableXrayVariant.Method, want); err != nil {
		t.Fatal(err)
	}
	root := newXrayConfigFor(stableXrayVariant, "private", "0123456789abcdef", "", nil)
	got, err := applyDesiredXrayRealitySNI(root, stableXrayVariant)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("desired state=%#v, %v", got, err)
	}
	reality, err := managedXrayRealitySettings(root, stableXrayVariant)
	if err != nil {
		t.Fatal(err)
	}
	names, err := xrayRealityServerNames(reality["serverNames"])
	if err != nil || !reflect.DeepEqual(names, want.ServerNames) {
		t.Fatalf("projected names=%#v, %v", names, err)
	}
	if reality["dest"] != want.Target {
		t.Fatalf("projected target=%#v", reality["dest"])
	}
}
