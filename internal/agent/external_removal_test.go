package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/silenceremember/sbp-panel/internal/config"
)

func TestExternalNetworkTuningRemovesPersistentConfiguration(t *testing.T) {
	settings := map[string]string{
		"/proc/sys/net/ipv4/tcp_congestion_control":           "bbr",
		"/proc/sys/net/ipv4/tcp_available_congestion_control": "cubic bbr",
		"/proc/sys/net/core/default_qdisc":                    "fq",
	}
	var rewritten []string
	ops := externalRemovalOps{
		run: func(_ string, args ...string) (string, error) {
			key, value, _ := strings.Cut(args[1], "=")
			paths := map[string]string{
				"net.core.default_qdisc":          "/proc/sys/net/core/default_qdisc",
				"net.ipv4.tcp_congestion_control": "/proc/sys/net/ipv4/tcp_congestion_control",
			}
			settings[paths[key]] = value
			return "", nil
		},
		kernelSetting:     func(path string) string { return settings[path] },
		tuningConfigFiles: func() ([]string, error) { return []string{"/etc/sysctl.conf"}, nil },
		rewriteTuning: func(paths []string) (func() error, error) {
			rewritten = append(rewritten, paths...)
			return func() error { return nil }, nil
		},
	}
	if _, err := removeExternalNetworkTuning(ops); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rewritten, []string{"/etc/sysctl.conf"}) {
		t.Fatalf("rewritten files = %#v", rewritten)
	}
}

func TestParseSysctlSetting(t *testing.T) {
	for _, test := range []struct {
		line      string
		wantKey   string
		wantValue string
	}{
		{line: "net.ipv4.tcp_congestion_control=bbr", wantKey: "net.ipv4.tcp_congestion_control", wantValue: "bbr"},
		{line: "-net.core.default_qdisc fq # provider default", wantKey: "net.core.default_qdisc", wantValue: "fq"},
		{line: "; disabled"},
	} {
		key, value := parseSysctlSetting(test.line)
		if key != test.wantKey || value != test.wantValue {
			t.Errorf("parseSysctlSetting(%q) = %q, %q; want %q, %q", test.line, key, value, test.wantKey, test.wantValue)
		}
	}
}

func TestRemoveExternalNetworkTuningLinesPreservesOtherContent(t *testing.T) {
	original := []byte("# provider settings\r\nnet.core.default_qdisc = fq\r\nnet.ipv4.tcp_congestion_control bbr # enabled\r\nnet.ipv4.ip_forward=1\r\nnet.core.default_qdisc=fq_codel\r\n# net.ipv4.tcp_congestion_control=bbr\r\n")
	want := []byte("# provider settings\r\nnet.ipv4.ip_forward=1\r\nnet.core.default_qdisc=fq_codel\r\n# net.ipv4.tcp_congestion_control=bbr\r\n")
	got, changed := removeExternalNetworkTuningLines(original)
	if !changed || !reflect.DeepEqual(got, want) {
		t.Fatalf("changed=%v\ngot=%q\nwant=%q", changed, got, want)
	}
}

func TestRewriteExternalNetworkTuningCanRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sysctl.conf")
	original := []byte("net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\nnet.ipv4.ip_forward=1\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	rollback, err := rewriteExternalNetworkTuning([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "net.ipv4.ip_forward=1\n" {
		t.Fatalf("updated file = %q", updated)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, original) {
		t.Fatalf("restored file = %q", restored)
	}
}

func TestFindExternalNetworkTuningFilesDeduplicatesSymlinks(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "sysctl.conf")
	link := filepath.Join(directory, "99-sysctl.conf")
	if err := os.WriteFile(target, []byte("net.ipv4.tcp_congestion_control=bbr\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	files, err := findExternalNetworkTuningFiles([]string{target, link})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{resolved}) {
		t.Fatalf("files = %#v, want %q", files, resolved)
	}
}

func TestExternalNetworkTuningResetsRuntimeState(t *testing.T) {
	settings := map[string]string{
		"/proc/sys/net/ipv4/tcp_congestion_control":           "bbr",
		"/proc/sys/net/ipv4/tcp_available_congestion_control": "reno cubic bbr",
		"/proc/sys/net/core/default_qdisc":                    "fq",
	}
	var commands [][]string
	ops := externalRemovalOps{
		run: func(name string, args ...string) (string, error) {
			commands = append(commands, append([]string{name}, args...))
			if len(args) != 2 || name != "sysctl" || args[0] != "-w" {
				return "", errors.New("unexpected command")
			}
			key, value, ok := strings.Cut(args[1], "=")
			if !ok {
				return "", errors.New("invalid sysctl")
			}
			paths := map[string]string{
				"net.core.default_qdisc":          "/proc/sys/net/core/default_qdisc",
				"net.ipv4.tcp_congestion_control": "/proc/sys/net/ipv4/tcp_congestion_control",
			}
			settings[paths[key]] = value
			return args[1], nil
		},
		kernelSetting:     func(path string) string { return settings[path] },
		tuningConfigFiles: func() ([]string, error) { return nil, nil },
	}
	if _, err := removeExternalNetworkTuning(ops); err != nil {
		t.Fatal(err)
	}
	if settings["/proc/sys/net/ipv4/tcp_congestion_control"] != "cubic" || settings["/proc/sys/net/core/default_qdisc"] != "fq_codel" {
		t.Fatalf("unexpected reset settings: %#v", settings)
	}
	want := [][]string{
		{"sysctl", "-w", "net.core.default_qdisc=fq_codel"},
		{"sysctl", "-w", "net.ipv4.tcp_congestion_control=cubic"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestExternalNetworkTuningRestoresBothSettingsAfterFailure(t *testing.T) {
	settings := map[string]string{
		"/proc/sys/net/ipv4/tcp_congestion_control":           "bbr",
		"/proc/sys/net/ipv4/tcp_available_congestion_control": "cubic bbr",
		"/proc/sys/net/core/default_qdisc":                    "fq",
	}
	failCC := true
	filesRestored := false
	ops := externalRemovalOps{
		run: func(name string, args ...string) (string, error) {
			key, value, _ := strings.Cut(args[1], "=")
			paths := map[string]string{
				"net.core.default_qdisc":          "/proc/sys/net/core/default_qdisc",
				"net.ipv4.tcp_congestion_control": "/proc/sys/net/ipv4/tcp_congestion_control",
			}
			settings[paths[key]] = value
			if key == "net.ipv4.tcp_congestion_control" && value == "cubic" && failCC {
				failCC = false
				return "partially applied", errors.New("sysctl failed")
			}
			return "", nil
		},
		kernelSetting:     func(path string) string { return settings[path] },
		tuningConfigFiles: func() ([]string, error) { return []string{"/etc/sysctl.conf"}, nil },
		rewriteTuning: func([]string) (func() error, error) {
			return func() error { filesRestored = true; return nil }, nil
		},
	}
	if _, err := removeExternalNetworkTuning(ops); err == nil {
		t.Fatal("expected external tuning reset failure")
	}
	if settings["/proc/sys/net/ipv4/tcp_congestion_control"] != "bbr" || settings["/proc/sys/net/core/default_qdisc"] != "fq" {
		t.Fatalf("settings were not restored: %#v", settings)
	}
	if !filesRestored {
		t.Fatal("persistent settings were not restored")
	}
}

func TestExternalDockerRequiresEmptyInventory(t *testing.T) {
	runs := 0
	ops := externalRemovalOps{
		lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		dockerInventory: func() error {
			return errors.New("remove all containers first: customer-container")
		},
		run: func(string, ...string) (string, error) {
			runs++
			return "", nil
		},
	}
	_, err := removeExternalDocker(ops)
	if err == nil || !strings.Contains(err.Error(), "customer-container") {
		t.Fatalf("non-empty Docker was not refused: %v", err)
	}
	if runs != 0 {
		t.Fatalf("Docker commands continued after inventory refusal: %d", runs)
	}
}

func TestExternalDockerRemovesOnlyVerifiedPackage(t *testing.T) {
	installed := true
	var commands [][]string
	ops := externalRemovalOps{
		lookPath: func(string) (string, error) {
			if installed {
				return "/usr/bin/docker", nil
			}
			return "", exec.ErrNotFound
		},
		installedPackages: func() (map[string]struct{}, error) {
			if installed {
				return map[string]struct{}{"docker.io": {}}, nil
			}
			return map[string]struct{}{}, nil
		},
		dockerInventory: func() error { return nil },
		run: func(name string, args ...string) (string, error) {
			commands = append(commands, append([]string{name}, args...))
			switch name {
			case "docker":
				return "", errors.New("Compose unavailable")
			case "dpkg-query":
				return "docker.io: /usr/bin/docker", nil
			case "systemctl":
				return "", nil
			case "apt-get":
				installed = false
				return "removed docker.io", nil
			default:
				return "", errors.New("unexpected command")
			}
		},
	}
	if _, err := removeExternalDocker(ops); err != nil {
		t.Fatal(err)
	}
	wantCommands := [][]string{
		{"docker", "compose", "version", "--short"},
		{"dpkg-query", "-S", "/usr/bin/docker"},
		{"systemctl", "is-active", "docker.service"},
		{"systemctl", "is-enabled", "docker.service"},
		{"systemctl", "disable", "--now", "docker.service", "docker.socket"},
		{"apt-get", "purge", "-y", "docker.io"},
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", commands, wantCommands)
	}
}

func TestExternalDockerRejectsUnsupportedPackageOwner(t *testing.T) {
	ops := externalRemovalOps{
		lookPath:        func(string) (string, error) { return "/usr/bin/docker", nil },
		dockerInventory: func() error { return nil },
		run: func(name string, args ...string) (string, error) {
			if name == "docker" {
				return "", errors.New("Compose unavailable")
			}
			if name == "dpkg-query" {
				return "docker-ce-cli: /usr/bin/docker", nil
			}
			return "", errors.New("unexpected command")
		},
	}
	_, err := removeExternalDocker(ops)
	if err == nil || !strings.Contains(err.Error(), "supported Ubuntu docker.io package") {
		t.Fatalf("unsupported package owner was not refused: %v", err)
	}
}

func TestExternalDockerRestoresServiceAfterPackageFailure(t *testing.T) {
	var commands [][]string
	ops := externalRemovalOps{
		lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		installedPackages: func() (map[string]struct{}, error) {
			return map[string]struct{}{"docker.io": {}}, nil
		},
		dockerInventory: func() error { return nil },
		run: func(name string, args ...string) (string, error) {
			commands = append(commands, append([]string{name}, args...))
			signature := strings.Join(append([]string{name}, args...), " ")
			switch signature {
			case "docker compose version --short":
				return "", errors.New("Compose unavailable")
			case "dpkg-query -S /usr/bin/docker":
				return "docker.io: /usr/bin/docker", nil
			case "systemctl is-active docker.service":
				return "active", nil
			case "systemctl is-enabled docker.service":
				return "enabled", nil
			case "systemctl disable --now docker.service docker.socket":
				return "", nil
			case "apt-get purge -y docker.io":
				return "package manager failed", errors.New("exit status 1")
			case "systemctl enable docker.service", "systemctl start docker.service":
				return "", nil
			default:
				return "", errors.New("unexpected command")
			}
		},
	}
	_, err := removeExternalDocker(ops)
	if err == nil || !strings.Contains(err.Error(), "remove external Docker packages") {
		t.Fatalf("package failure was not returned: %v", err)
	}
	wantTail := [][]string{
		{"systemctl", "enable", "docker.service"},
		{"systemctl", "start", "docker.service"},
	}
	if len(commands) < len(wantTail) || !reflect.DeepEqual(commands[len(commands)-len(wantTail):], wantTail) {
		t.Fatalf("service state was not restored: %#v", commands)
	}
}

func TestExternalDockerRefusesExternalComposePlugin(t *testing.T) {
	queries := 0
	ops := externalRemovalOps{
		lookPath:        func(string) (string, error) { return "/usr/bin/docker", nil },
		dockerInventory: func() error { return nil },
		run: func(name string, args ...string) (string, error) {
			if name == "docker" && strings.Join(args, " ") == "compose version --short" {
				return "v2.37.1", nil
			}
			queries++
			return "", errors.New("unexpected command")
		},
	}
	_, err := removeExternalDocker(ops)
	if err == nil || !strings.Contains(err.Error(), "Compose CLI plugin") {
		t.Fatalf("external Compose plugin was not refused: %v", err)
	}
	if queries != 0 {
		t.Fatalf("Docker removal continued after Compose detection: %d commands", queries)
	}
}

func TestExternalRemovalRejectsOwnedComponent(t *testing.T) {
	isolateComponentOwnership(t)
	if err := markComponentOwned("docker", nil); err != nil {
		t.Fatal(err)
	}
	_, err := removeExternalComponent("docker", config.Config{})
	if err == nil || !strings.Contains(err.Error(), "managed by SBP") {
		t.Fatalf("owned component was accepted for external removal: %v", err)
	}
}
