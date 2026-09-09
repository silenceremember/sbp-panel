package agent

import (
	"errors"
	"os"
	"testing"
)

func TestXrayFingerprintSettingsDoNotRestartServer(t *testing.T) {
	variant, _ := testXraySNIVariant(t, stableXrayVariant)
	ops := testXraySNIOps(func(string) error { t.Fatal("client fingerprint required config validation"); return nil }, func(xrayVariant, map[string]any) error { t.Fatal("fingerprint restarted the server"); return nil })
	state, err := mutateXrayRealitySettings(variant, xrayRealityTarget, []string{xrayRealityServerName}, ops, "firefox")
	if err != nil || state.Fingerprint != "firefox" {
		t.Fatalf("%#v %v", state, err)
	}
	metadata, err := variant.loadClientMetadata()
	if err != nil || metadata.Fingerprint != "firefox" {
		t.Fatalf("new profiles lost fingerprint: %#v %v", metadata, err)
	}
}

func TestXrayTextSettingsRollbackFailedApply(t *testing.T) {
	variant, before := testXraySNIVariant(t, xhttpXrayVariant)
	attempts := 0
	ops := testXraySNIOps(func(string) error { return nil }, func(xrayVariant, map[string]any) error {
		attempts++
		if attempts == 1 {
			return errors.New("unhealthy candidate")
		}
		return nil
	})
	_, err := mutateXrayRealitySettings(variant, "dl.google.com:443", []string{"dl.google.com"}, ops, "firefox")
	if err == nil || attempts != 2 {
		t.Fatalf("rollback: %v attempts=%d", err, attempts)
	}
	after, _ := os.ReadFile(variant.ConfigFile)
	if string(after) != string(before) {
		t.Fatal("working server configuration not restored")
	}
}

func TestXrayTextSettingsValidation(t *testing.T) {
	valid := `{"default_sni":"dl.google.com","server_names":["dl.google.com"],"target":"dl.google.com:443","fingerprint":"firefox"}`
	state, err := parseXrayTextSettings(valid)
	if err != nil || state.DefaultSNI != "dl.google.com" {
		t.Fatal(state, err)
	}
	if err := validateConfigurationSettings(map[string]string{"xray": `{"target":"invalid"}`}); err == nil {
		t.Fatal("invalid restored settings accepted")
	}
	if _, err := parseXrayTextSettings(valid + `{}`); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
