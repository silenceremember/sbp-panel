package agent

import "testing"

func TestParseOSReleasePrefersPrettyName(t *testing.T) {
	release := []byte("NAME=Ubuntu\nVERSION=\"24.04.4 LTS (Noble Numbat)\"\nPRETTY_NAME=\"Ubuntu 24.04.4 LTS\"\n")
	if got := parseOSRelease(release); got != "Ubuntu 24.04.4 LTS" {
		t.Fatalf("parseOSRelease() = %q", got)
	}
}

func TestParseOSReleaseFallsBackToNameAndVersion(t *testing.T) {
	release := []byte("NAME='Example Linux'\nVERSION=1.2\n")
	if got := parseOSRelease(release); got != "Example Linux 1.2" {
		t.Fatalf("parseOSRelease() = %q", got)
	}
}
