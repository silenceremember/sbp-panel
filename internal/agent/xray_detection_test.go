package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func existingXrayStatus(containerList, configPath string) (bool, error) {
	return existingXrayVariantStatus(stableXrayVariant, containerList, configPath)
}

func TestExistingXrayStatus(t *testing.T) {
	tests := []struct {
		name       string
		containers string
		configPath string
		present    bool
		errorText  string
	}{
		{
			name:       "managed installation",
			containers: "xray-stable\namnezia-awg2",
			configPath: "/opt/vpn-panel-managed/xray/config.json",
			present:    true,
		},
		{
			name:       "known container without supported config",
			containers: "xray-stable",
			errorText:  "supported SBP paths",
		},
		{
			name:       "external container",
			containers: "customer-xray",
			errorText:  "external Xray container",
		},
		{
			name:       "unrelated containers",
			containers: "nginx\namnezia-awg2",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			present, err := existingXrayStatus(test.containers, test.configPath)
			if present != test.present {
				t.Fatalf("present = %v, want %v", present, test.present)
			}
			if test.errorText == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.errorText) {
				t.Fatalf("error = %v, want text %q", err, test.errorText)
			}
		})
	}
}

func TestWriteXrayConfigIsReadableByContainerUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeXrayConfig(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("config mode = %04o, want 0644", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Fatalf("config body = %q, want new", body)
	}
}
