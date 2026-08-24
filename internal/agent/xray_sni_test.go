package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testXraySNIVariant(t *testing.T, base xrayVariant) (xrayVariant, []byte) {
	t.Helper()
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

func TestDecodeXrayRealitySNIRequestIsBoundedAndExact(t *testing.T) {
	if got, err := decodeXrayRealitySNIRequest(strings.NewReader(`{"sni":"dl.google.com"}`)); err != nil || got != "dl.google.com" {
		t.Fatalf("decoded SNI=%q err=%v", got, err)
	}
	for _, body := range []string{"", `{"sni":`, `{"sni":"dl.google.com"} trailing`, strings.Repeat("x", 4<<10+1)} {
		if _, err := decodeXrayRealitySNIRequest(strings.NewReader(body)); err == nil {
			t.Fatalf("invalid request with %d bytes was accepted", len(body))
		}
	}
}

func TestDecodeXrayRealityTargetRequestIsBoundedAndExact(t *testing.T) {
	if got, err := decodeXrayRealityTargetRequest(strings.NewReader(`{"target":"dl.google.com:443"}`)); err != nil || got != "dl.google.com:443" {
		t.Fatalf("decoded target=%q err=%v", got, err)
	}
	for _, body := range []string{"", `{"target":`, strings.Repeat("x", 4<<10+1)} {
		if _, err := decodeXrayRealityTargetRequest(strings.NewReader(body)); err == nil {
			t.Fatalf("invalid target request with %d bytes was accepted", len(body))
		}
	}
}

func TestAddXrayRealitySNIPreservesDefaultTargetClientsAndMetadata(t *testing.T) {
	variant, _ := testXraySNIVariant(t, xhttpXrayVariant)
	metadataBefore, _ := os.ReadFile(variant.MetadataFile)
	validated := false
	restarted := false
	ops := testXraySNIOps(func(path string) error {
		validated = true
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(body), "dl.google.com") {
			return errors.New("staged SNI missing")
		}
		return nil
	}, func(got xrayVariant, root map[string]any) error {
		restarted = true
		if got.Method != variant.Method {
			return errors.New("wrong variant restarted")
		}
		return nil
	})

	state, err := mutateXrayRealitySNI(variant, "DL.Google.COM.", false, ops)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{xrayRealityServerName, "dl.google.com"}
	if state.DefaultSNI != xrayRealityServerName || !reflect.DeepEqual(state.ServerNames, wantNames) || !validated || !restarted {
		t.Fatalf("state=%#v validated=%v restarted=%v", state, validated, restarted)
	}
	root, _, stored, err := readXrayRealitySNIState(variant)
	if err != nil {
		t.Fatal(err)
	}
	reality, _ := managedXrayRealitySettings(root, variant)
	if reality["target"] != xrayRealityTarget || !reflect.DeepEqual(stored.ServerNames, wantNames) {
		t.Fatalf("stored REALITY settings=%#v state=%#v", reality, stored)
	}
	inbound, _, _ := managedXrayInbound(root)
	if clients := inbound["settings"].(map[string]any)["clients"].([]any); len(clients) != 1 {
		t.Fatalf("clients changed: %#v", clients)
	}
	metadataAfter, _ := os.ReadFile(variant.MetadataFile)
	if !reflect.DeepEqual(metadataAfter, metadataBefore) {
		t.Fatal("client metadata changed while adding an allowed SNI")
	}
}

func TestXrayRealitySNIMutationIsIdempotentAndProtectsDefault(t *testing.T) {
	variant, before := testXraySNIVariant(t, stableXrayVariant)
	calls := 0
	ops := testXraySNIOps(func(string) error { calls++; return nil }, func(xrayVariant, map[string]any) error { calls++; return nil })
	state, err := mutateXrayRealitySNI(variant, xrayRealityServerName, false, ops)
	if err != nil || len(state.ServerNames) != 1 || calls != 0 {
		t.Fatalf("idempotent add=%#v err=%v calls=%d", state, err, calls)
	}
	if _, err := mutateXrayRealitySNI(variant, xrayRealityServerName, true, ops); err == nil || !strings.Contains(err.Error(), "cannot be removed") {
		t.Fatalf("default removal error=%v", err)
	}
	after, _ := os.ReadFile(variant.ConfigFile)
	if !reflect.DeepEqual(after, before) || calls != 0 {
		t.Fatal("protected or idempotent mutation changed the configuration")
	}
}

func TestRemoveAdditionalXrayRealitySNIKeepsDefault(t *testing.T) {
	variant, _ := testXraySNIVariant(t, stableXrayVariant)
	ops := testXraySNIOps(func(string) error { return nil }, func(xrayVariant, map[string]any) error { return nil })
	if _, err := mutateXrayRealitySNI(variant, "dl.google.com", false, ops); err != nil {
		t.Fatal(err)
	}
	state, err := mutateXrayRealitySNI(variant, "dl.google.com", true, ops)
	if err != nil {
		t.Fatal(err)
	}
	if state.DefaultSNI != xrayRealityServerName || !reflect.DeepEqual(state.ServerNames, []string{xrayRealityServerName}) {
		t.Fatalf("state after removal=%#v", state)
	}
	_, _, stored, err := readXrayRealitySNIState(variant)
	if err != nil || !reflect.DeepEqual(stored.ServerNames, []string{xrayRealityServerName}) {
		t.Fatalf("stored state after removal=%#v err=%v", stored, err)
	}
}

func TestXrayRealitySNIRollsBackFailedRestart(t *testing.T) {
	variant, before := testXraySNIVariant(t, stableXrayVariant)
	restarts := 0
	ops := testXraySNIOps(func(string) error { return nil }, func(xrayVariant, map[string]any) error {
		restarts++
		if restarts == 1 {
			return errors.New("new runtime failed")
		}
		return nil
	})
	if _, err := mutateXrayRealitySNI(variant, "dl.google.com", false, ops); err == nil || !strings.Contains(err.Error(), "previous configuration was restored") {
		t.Fatalf("rollback result=%v", err)
	}
	after, _ := os.ReadFile(variant.ConfigFile)
	if !reflect.DeepEqual(after, before) || restarts != 2 {
		t.Fatalf("rollback did not restore bytes or runtime: restarts=%d", restarts)
	}
}

func TestXrayRealitySNIValidationFailureDoesNotApply(t *testing.T) {
	variant, before := testXraySNIVariant(t, stableXrayVariant)
	restarts := 0
	ops := testXraySNIOps(func(string) error { return errors.New("invalid staged config") }, func(xrayVariant, map[string]any) error {
		restarts++
		return nil
	})
	if _, err := mutateXrayRealitySNI(variant, "dl.google.com", false, ops); err == nil || !strings.Contains(err.Error(), "invalid staged config") {
		t.Fatalf("validation result=%v", err)
	}
	after, _ := os.ReadFile(variant.ConfigFile)
	if !reflect.DeepEqual(after, before) || restarts != 0 {
		t.Fatal("validation failure changed persistent or runtime state")
	}
}

func TestXrayRealitySNISavesDesiredStateBeforeInstall(t *testing.T) {
	variant, before := testXraySNIVariant(t, stableXrayVariant)
	originalSettingsDir := componentSettingsDir
	componentSettingsDir = filepath.Join(t.TempDir(), "settings")
	t.Cleanup(func() { componentSettingsDir = originalSettingsDir })
	ops := testXraySNIOps(func(string) error { return nil }, func(xrayVariant, map[string]any) error { return nil })
	ops.owned = func(string) bool { return false }
	ops.verifyContainer = func(xrayVariant) error { t.Fatal("unowned container was inspected for mutation"); return nil }
	state, err := mutateXrayRealitySNI(variant, "dl.google.com", false, ops)
	if err != nil || len(state.ServerNames) != 2 || state.ServerNames[1] != "dl.google.com" {
		t.Fatalf("desired mutation result=%#v, %v", state, err)
	}
	after, _ := os.ReadFile(variant.ConfigFile)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("unowned configuration changed")
	}
	saved, _, err := loadDesiredXrayRealitySNIState(variant.Method)
	if err != nil || !reflect.DeepEqual(saved, state) {
		t.Fatalf("saved desired state=%#v, %v", saved, err)
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

func TestSetXrayRealityTargetValidatesAndPreservesProfiles(t *testing.T) {
	variant, _ := testXraySNIVariant(t, xhttpXrayVariant)
	metadataBefore, _ := os.ReadFile(variant.MetadataFile)
	validated := ""
	ops := testXraySNIOps(func(string) error { return nil }, func(xrayVariant, map[string]any) error { return nil })
	ops.validateTarget = func(target string) error { validated = target; return nil }
	state, err := mutateXrayRealityTarget(variant, "DL.Google.COM:443", ops)
	if err != nil || state.Target != "dl.google.com:443" || validated != state.Target {
		t.Fatalf("target state=%#v validated=%q err=%v", state, validated, err)
	}
	root, _, stored, err := readXrayRealitySNIState(variant)
	if err != nil || stored.Target != state.Target {
		t.Fatalf("stored target=%#v err=%v", stored, err)
	}
	reality, _ := managedXrayRealitySettings(root, variant)
	if reality["target"] != state.Target || reality["dest"] != nil {
		t.Fatalf("REALITY target fields=%#v", reality)
	}
	metadataAfter, _ := os.ReadFile(variant.MetadataFile)
	if !reflect.DeepEqual(metadataAfter, metadataBefore) {
		t.Fatal("client metadata changed while updating the target")
	}
}

func TestSetXrayRealityTargetProbeFailureDoesNotApply(t *testing.T) {
	variant, before := testXraySNIVariant(t, stableXrayVariant)
	ops := testXraySNIOps(func(string) error { return nil }, func(xrayVariant, map[string]any) error {
		t.Fatal("runtime restarted after a failed target probe")
		return nil
	})
	ops.validateTarget = func(string) error { return errors.New("TLS probe failed") }
	if _, err := mutateXrayRealityTarget(variant, "dl.google.com:443", ops); err == nil || !strings.Contains(err.Error(), "TLS probe failed") {
		t.Fatalf("probe failure=%v", err)
	}
	after, _ := os.ReadFile(variant.ConfigFile)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("failed target probe changed the configuration")
	}
}
