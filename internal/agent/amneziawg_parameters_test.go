package agent

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewAmneziaWG3Settings(t *testing.T) {
	seen := map[string]bool{}
	for range 32 {
		generated, err := newAmneziaWG3Settings()
		if err != nil {
			t.Fatal(err)
		}
		settings, err := parseAmneziaWGServerSettings(generated.server)
		if err != nil {
			t.Fatal(err)
		}
		if settings.Jc != 6 || settings.S1 != 12 || settings.S2 != 12 || settings.S3 != 12 || settings.S4 != 12 || settings.H1 != "1" || settings.H2 != "2" || settings.H3 != "3" || settings.H4 != "4" {
			t.Fatal("generated configuration does not use the compatible AWG 3.1 defaults")
		}
		key, err := base64.StdEncoding.DecodeString(settings.HeaderProtectionKey)
		if err != nil || len(key) != 32 || seen[settings.HeaderProtectionKey] {
			t.Fatal("header protection key missing or reused")
		}
		seen[settings.HeaderProtectionKey] = true
		if settings.RandomTrailers != "off" || settings.DisableCookies != "off" || generated.client != generated.server+"I1 = "+amneziaWGDefaultI1+"\n" {
			t.Fatal("unexpected client/server defaults")
		}
	}
}

func TestAmneziaWGSettingsRejectUnsafeTrailers(t *testing.T) {
	defaults, err := generatedAmneziaWGServerSettings()
	if err != nil {
		t.Fatal(err)
	}
	defaults.RandomTrailers = "on"
	if err := validateAmneziaWGServerSettings(defaults); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*amneziaWGServerSettings){
		func(s *amneziaWGServerSettings) { s.S2 = 13 },
		func(s *amneziaWGServerSettings) { s.H4 = "4-500" },
		func(s *amneziaWGServerSettings) { s.H2 = s.H1 },
		func(s *amneziaWGServerSettings) { s.HeaderProtectionKey = "" },
	} {
		candidate := defaults
		change(&candidate)
		if validateAmneziaWGServerSettings(candidate) == nil {
			t.Fatal("unsafe settings accepted")
		}
	}
	if _, err := parseAmneziaWGServerSettings(strings.Replace(canonicalAmneziaWGServerSettings(defaults), "RandomTrailers = on\n", "", 1)); err == nil {
		t.Fatal("missing required field accepted")
	}
}

func TestAmneziaWGHeaderValues(t *testing.T) {
	for _, value := range []string{"1", "4294967295", "100-200"} {
		if _, _, err := parseAmneziaWGHeaderRange(value); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"", "-1", "4294967296", "200-100", "a", "1-2-3"} {
		if _, _, err := parseAmneziaWGHeaderRange(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
