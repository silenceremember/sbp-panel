package agent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func stubDockerCommand(t *testing.T, fn func(args ...string) (string, error)) {
	t.Helper()
	original := dockerCommand
	dockerCommand = fn
	t.Cleanup(func() { dockerCommand = original })
}

func TestDockerInventoryFailureBlocksUninstall(t *testing.T) {
	calls := 0
	stubDockerCommand(t, func(args ...string) (string, error) {
		calls++
		return "daemon unavailable", errors.New("exit status 1")
	})

	err := requireEmptyDockerInventory()
	if err == nil || !strings.Contains(err.Error(), "inventory Docker containers") {
		t.Fatalf("inventory failure was not returned: %v", err)
	}
	if calls != 1 {
		t.Fatalf("inventory continued after failure: %d calls", calls)
	}
}

func TestDockerInventoryAllowsOnlyBuiltInNetworks(t *testing.T) {
	var calls [][]string
	stubDockerCommand(t, func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 0 && args[0] == "network" {
			return "bridge\nhost\nnone\n", nil
		}
		return "", nil
	})

	if err := requireEmptyDockerInventory(); err != nil {
		t.Fatalf("empty inventory was rejected: %v", err)
	}
	want := [][]string{
		{"ps", "-a", "--format", "{{.Names}}"},
		{"image", "ls", "-q"},
		{"volume", "ls", "-q"},
		{"network", "ls", "--format", "{{.Name}}"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unexpected inventory commands:\n got: %#v\nwant: %#v", calls, want)
	}
}

func TestDockerPathOwnershipRecordsExistingAndAbsentPaths(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	absent := filepath.Join(root, "absent")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	previous := map[string]string{}
	if err := recordPathOwnership(previous, []string{existing, absent}); err != nil {
		t.Fatal(err)
	}
	if previous[dockerPathOwnershipPrefix+existing] != "present" || previous[dockerPathOwnershipPrefix+absent] != "absent" {
		t.Fatalf("unexpected path ownership: %#v", previous)
	}
}

func TestDockerPathCleanupRequiresExplicitAbsentEvidence(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "owned")
	preexisting := filepath.Join(root, "preexisting")
	unknown := filepath.Join(root, "unknown")
	for _, path := range []string{owned, preexisting, unknown} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	previous := map[string]string{
		dockerPathOwnershipPrefix + owned:       "absent",
		dockerPathOwnershipPrefix + preexisting: "present",
	}
	if err := removePreviouslyAbsentPaths(previous, []string{owned, preexisting, unknown}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("proven SBP-created path was not removed: %v", err)
	}
	for _, path := range []string{preexisting, unknown} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("path without absent evidence was removed: %s: %v", path, err)
		}
	}
}

func TestStrictDockerRemovalOnlyIgnoresTypedNotFound(t *testing.T) {
	stubDockerCommand(t, func(args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "rm -f missing-container":
			return "Error response from daemon: No such container: missing-container", errors.New("exit status 1")
		case "image rm -f missing-image":
			return "Error response from daemon: No such image: missing-image", errors.New("exit status 1")
		case "rm -f denied-container":
			return "permission denied", errors.New("exit status 1")
		case "image rm -f denied-image":
			return "conflict: image is being used", errors.New("exit status 1")
		default:
			return "", nil
		}
	})

	if err := removeContainersStrict("missing-container"); err != nil {
		t.Fatalf("missing container was not idempotent: %v", err)
	}
	if err := removeImagesStrict("missing-image"); err != nil {
		t.Fatalf("missing image was not idempotent: %v", err)
	}
	if err := removeContainersStrict("denied-container"); err == nil {
		t.Fatal("container removal failure was ignored")
	}
	if err := removeImagesStrict("denied-image"); err == nil {
		t.Fatal("image removal failure was ignored")
	}
}

func TestCleanupInterruptedInstallPreservesStateOnDockerFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "component")
	if err := writeInstallMarker(dir, installMarker{Containers: []string{"managed-container"}, Images: []string{"managed-image"}}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	stubDockerCommand(t, func(args ...string) (string, error) {
		return "permission denied", errors.New("exit status 1")
	})

	if err := cleanupInterruptedInstall(dir); err == nil {
		t.Fatal("cleanup unexpectedly succeeded")
	}
	for _, path := range []string{filepath.Join(dir, installMarkerName), configPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("recovery state %q was not preserved: %v", path, err)
		}
	}
}

func TestCleanupInterruptedInstallTreatsMissingResourcesAsClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "component")
	if err := writeInstallMarker(dir, installMarker{Containers: []string{"missing-container"}, Images: []string{"missing-image"}}); err != nil {
		t.Fatal(err)
	}
	stubDockerCommand(t, func(args ...string) (string, error) {
		if len(args) > 0 && args[0] == "rm" {
			return "Error response from daemon: No such container", errors.New("exit status 1")
		}
		return "Error response from daemon: No such image", errors.New("exit status 1")
	})

	if err := cleanupInterruptedInstall(dir); err != nil {
		t.Fatalf("idempotent cleanup failed: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("completed cleanup left state behind: %v", err)
	}
}

func TestRemoveBypassContainersUsesExactManagedNames(t *testing.T) {
	var removed []string
	stubDockerCommand(t, func(args ...string) (string, error) {
		if reflect.DeepEqual(args, []string{"ps", "-a", "--format", "{{.Names}}"}) {
			return strings.Join([]string{
				"vpn-panel-bypass-wb-g1",
				"vpn-panel-bypass-wb-g27-init",
				"vpn-panel-bypass-wb-g0",
				"vpn-panel-bypass-wb-g1-copy",
				"vpn-panel-bypass-wb-external",
			}, "\n"), nil
		}
		if len(args) == 3 && args[0] == "rm" && args[1] == "-f" {
			removed = append(removed, args[2])
			return "", nil
		}
		return "", errors.New("unexpected Docker command")
	})

	if err := removeBypassContainersStrict("wb"); err != nil {
		t.Fatal(err)
	}
	want := []string{"vpn-panel-bypass-wb-g1", "vpn-panel-bypass-wb-g27-init"}
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed containers = %#v, want %#v", removed, want)
	}
}

func TestMissingOwnedXrayContainerKeepsUninstallRetryable(t *testing.T) {
	stubDockerCommand(t, func(args ...string) (string, error) {
		if reflect.DeepEqual(args, []string{"ps", "-a", "--format", "{{.Names}}"}) {
			return "", nil
		}
		return "", errors.New("unexpected Docker command")
	})
	if err := verifyManagedXrayContainerIfPresent(stableXrayVariant); err != nil {
		t.Fatalf("missing container blocked an idempotent uninstall retry: %v", err)
	}
}
