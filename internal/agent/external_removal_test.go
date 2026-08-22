package agent

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/silenceremember/sbp-panel/internal/config"
)

func TestExternalNetworkTuningRefusesPersistentConfiguration(t *testing.T) {
	runs := 0
	ops := externalRemovalOps{
		run: func(string, ...string) (string, error) {
			runs++
			return "", nil
		},
		kernelSetting: func(path string) string {
			if strings.HasSuffix(path, "tcp_congestion_control") {
				return "bbr"
			}
			return ""
		},
		tuningConfigFiles: func() ([]string, error) { return []string{"/etc/sysctl.d/provider.conf"}, nil },
	}
	_, err := removeExternalNetworkTuning(ops)
	if err == nil || !strings.Contains(err.Error(), "/etc/sysctl.d/provider.conf") {
		t.Fatalf("persistent external tuning was not refused: %v", err)
	}
	if runs != 0 {
		t.Fatalf("sysctl changed despite persistent external configuration: %d calls", runs)
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
		tuningConfigFiles: func() ([]string, error) { return nil, nil },
	}
	if _, err := removeExternalNetworkTuning(ops); err == nil {
		t.Fatal("expected external tuning reset failure")
	}
	if settings["/proc/sys/net/ipv4/tcp_congestion_control"] != "bbr" || settings["/proc/sys/net/core/default_qdisc"] != "fq" {
		t.Fatalf("settings were not restored: %#v", settings)
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
