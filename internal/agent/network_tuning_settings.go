package agent

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

const (
	networkTuningModulePath = "/etc/modules-load.d/sbp-bbr.conf"
	networkTuningSysctlPath = "/etc/sysctl.d/99-sbp-network.conf"
)

var networkTuningValuePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

type componentTextSettingsState struct {
	ComponentID    string `json:"component_id"`
	Content        string `json:"content"`
	DefaultContent string `json:"default_content"`
	Installed      bool   `json:"installed"`
	External       bool   `json:"external"`
	Editable       bool   `json:"editable"`
	Notice         string `json:"notice"`
	Warning        string `json:"warning,omitempty"`
}

type networkTuningSettings struct {
	DefaultQdisc      string
	CongestionControl string
}

type networkTuningApplyOps struct {
	readFile      func(string) ([]byte, error)
	writeFile     func(string, []byte, os.FileMode) error
	removeFile    func(string) error
	run           func(string, ...string) (string, error)
	kernelSetting func(string) string
}

func defaultNetworkTuningSettings() networkTuningSettings {
	return networkTuningSettings{DefaultQdisc: "fq", CongestionControl: "bbr"}
}

func defaultNetworkTuningApplyOps() networkTuningApplyOps {
	return networkTuningApplyOps{
		readFile:      os.ReadFile,
		writeFile:     replaceSettingsFile,
		removeFile:    os.Remove,
		run:           run,
		kernelSetting: kernelSetting,
	}
}

func canonicalNetworkTuningSettings(settings networkTuningSettings) string {
	return "modprobe tcp_bbr\n" +
		"sysctl -w net.core.default_qdisc=" + settings.DefaultQdisc + "\n" +
		"sysctl -w net.ipv4.tcp_congestion_control=" + settings.CongestionControl + "\n"
}

func parseNetworkTuningSettings(content string) (networkTuningSettings, error) {
	settings := defaultNetworkTuningSettings()
	seen := map[string]bool{}
	for lineNumber, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "modprobe tcp_bbr" {
			if seen["module"] {
				return networkTuningSettings{}, fmt.Errorf("line %d duplicates modprobe tcp_bbr", lineNumber+1)
			}
			seen["module"] = true
			continue
		}
		const prefix = "sysctl -w "
		if !strings.HasPrefix(line, prefix) {
			return networkTuningSettings{}, fmt.Errorf("line %d is not an allowed network tuning command", lineNumber+1)
		}
		key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, prefix)), "=")
		if !ok || !networkTuningValuePattern.MatchString(value) {
			return networkTuningSettings{}, fmt.Errorf("line %d has an invalid sysctl value", lineNumber+1)
		}
		key = strings.TrimSpace(key)
		if seen[key] {
			return networkTuningSettings{}, fmt.Errorf("line %d duplicates %s", lineNumber+1, key)
		}
		seen[key] = true
		switch key {
		case "net.core.default_qdisc":
			if value != "fq" && value != "fq_codel" {
				return networkTuningSettings{}, errors.New("default_qdisc must be fq or fq_codel")
			}
			settings.DefaultQdisc = value
		case "net.ipv4.tcp_congestion_control":
			if value != "bbr" && value != "cubic" && value != "reno" {
				return networkTuningSettings{}, errors.New("tcp_congestion_control must be bbr, cubic, or reno")
			}
			settings.CongestionControl = value
		default:
			return networkTuningSettings{}, fmt.Errorf("%s is not managed by Network tuning", key)
		}
	}
	return settings, nil
}

func loadNetworkTuningSettings() (networkTuningSettings, error) {
	body, exists, err := readComponentSettings("tweaks")
	if err != nil {
		return networkTuningSettings{}, err
	}
	if !exists {
		return defaultNetworkTuningSettings(), nil
	}
	return parseNetworkTuningSettings(string(body))
}

func networkTuningSettingsState() (componentTextSettingsState, error) {
	settings, err := loadNetworkTuningSettings()
	if err != nil {
		return componentTextSettingsState{}, err
	}
	_, installed := componentOwnership("tweaks")
	external := !installed && kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control") == "bbr"
	state := componentTextSettingsState{
		ComponentID:    "tweaks",
		Content:        canonicalNetworkTuningSettings(settings),
		DefaultContent: canonicalNetworkTuningSettings(defaultNetworkTuningSettings()),
		Installed:      installed,
		External:       external,
		Editable:       true,
		Notice:         "These are global desired server settings. They remain available before installation, are used by a later install, and can be saved again after installation.",
	}
	if external {
		state.Warning = "External network tuning is active. Saving changes records desired SBP settings for a later install and does not mutate external configuration."
	}
	return state, nil
}

func replaceSettingsFile(path string, body []byte, mode os.FileMode) error {
	temporary := path + ".settings-next"
	_ = os.Remove(temporary)
	if err := os.WriteFile(temporary, body, mode); err != nil {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_RDWR, 0)
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return errors.Join(syncErr, closeErr)
	}
	if err := os.Rename(temporary, path); err != nil {
		if runtime.GOOS == "windows" {
			if removeErr := os.Remove(path); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				err = os.Rename(temporary, path)
			}
		}
		if err != nil {
			_ = os.Remove(temporary)
			return err
		}
	}
	return syncParentDirectory(path)
}

func applyNetworkTuningSettings(settings networkTuningSettings, ops networkTuningApplyOps) (string, error) {
	previousModule, moduleErr := ops.readFile(networkTuningModulePath)
	if moduleErr != nil && !errors.Is(moduleErr, os.ErrNotExist) {
		return "", moduleErr
	}
	previousSysctl, sysctlErr := ops.readFile(networkTuningSysctlPath)
	if sysctlErr != nil && !errors.Is(sysctlErr, os.ErrNotExist) {
		return "", sysctlErr
	}
	previousCC := ops.kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control")
	previousQdisc := ops.kernelSetting("/proc/sys/net/core/default_qdisc")
	rollback := func() error {
		var rollbackErrors []error
		if moduleErr == nil {
			rollbackErrors = append(rollbackErrors, ops.writeFile(networkTuningModulePath, previousModule, 0644))
		} else if err := ops.removeFile(networkTuningModulePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, err)
		}
		if sysctlErr == nil {
			rollbackErrors = append(rollbackErrors, ops.writeFile(networkTuningSysctlPath, previousSysctl, 0644))
		} else if err := ops.removeFile(networkTuningSysctlPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, err)
		}
		if _, err := ops.run("sysctl", "--system"); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
		if previousQdisc != "" {
			_, err := ops.run("sysctl", "-w", "net.core.default_qdisc="+previousQdisc)
			rollbackErrors = append(rollbackErrors, err)
		}
		if previousCC != "" {
			_, err := ops.run("sysctl", "-w", "net.ipv4.tcp_congestion_control="+previousCC)
			rollbackErrors = append(rollbackErrors, err)
		}
		return errors.Join(rollbackErrors...)
	}
	if err := ops.writeFile(networkTuningModulePath, []byte("tcp_bbr\n"), 0644); err != nil {
		return "", err
	}
	if settings.CongestionControl == "bbr" {
		if output, err := ops.run("modprobe", "tcp_bbr"); err != nil {
			return output, errors.Join(err, rollback())
		}
	}
	body := []byte("net.core.default_qdisc=" + settings.DefaultQdisc + "\nnet.ipv4.tcp_congestion_control=" + settings.CongestionControl + "\n")
	if err := ops.writeFile(networkTuningSysctlPath, body, 0644); err != nil {
		return "", errors.Join(err, rollback())
	}
	out, err := ops.run("sysctl", "--system")
	if err != nil {
		return out, errors.Join(err, rollback())
	}
	if actual := ops.kernelSetting("/proc/sys/net/core/default_qdisc"); actual != settings.DefaultQdisc {
		return out, errors.Join(fmt.Errorf("default_qdisc is %q, want %q", actual, settings.DefaultQdisc), rollback())
	}
	if actual := ops.kernelSetting("/proc/sys/net/ipv4/tcp_congestion_control"); actual != settings.CongestionControl {
		return out, errors.Join(fmt.Errorf("tcp_congestion_control is %q, want %q", actual, settings.CongestionControl), rollback())
	}
	return out, nil
}

func saveNetworkTuningSettings(content string) (componentTextSettingsState, error) {
	settings, err := parseNetworkTuningSettings(content)
	if err != nil {
		return componentTextSettingsState{}, err
	}
	canonical := []byte(canonicalNetworkTuningSettings(settings))
	componentSettingsMu.Lock()
	defer componentSettingsMu.Unlock()
	previous, existed, err := readComponentSettings("tweaks")
	if err != nil {
		return componentTextSettingsState{}, err
	}
	if err := writeComponentSettings("tweaks", canonical); err != nil {
		return componentTextSettingsState{}, err
	}
	if _, installed := componentOwnership("tweaks"); installed {
		if _, err := applyNetworkTuningSettings(settings, defaultNetworkTuningApplyOps()); err != nil {
			restoreErr := restoreComponentSettings("tweaks", previous, existed)
			return componentTextSettingsState{}, errors.Join(err, restoreErr)
		}
	}
	return networkTuningSettingsState()
}
