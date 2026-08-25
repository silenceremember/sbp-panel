package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type amneziaWGServerSettings struct {
	Jc                  int
	Jmin                int
	Jmax                int
	S1                  int
	S2                  int
	S3                  int
	S4                  int
	H1                  string
	H2                  string
	H3                  string
	H4                  string
	HeaderProtectionKey string
	RandomTrailers      string
	DisableCookies      string
}

type amneziaWGSettingsApplyOps struct {
	readServer      func() ([]byte, error)
	readMetadata    func() ([]byte, error)
	applyServer     func([]byte) error
	replaceMetadata func([]byte) error
}

var amneziaWGSettingOrder = []string{
	"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4",
	"HeaderProtectionKey", "RandomTrailers", "DisableCookies",
}

var amneziaWG3SettingOrder = amneziaWGSettingOrder[11:]

func defaultAmneziaWGServerSettingsContent() string {
	var result strings.Builder
	for _, key := range amneziaWGSettingOrder {
		result.WriteString(key)
		result.WriteString(" = auto\n")
	}
	return result.String()
}

func canonicalAmneziaWGServerSettings(settings amneziaWGServerSettings) string {
	base := fmt.Sprintf(
		"Jc = %d\nJmin = %d\nJmax = %d\nS1 = %d\nS2 = %d\nS3 = %d\nS4 = %d\nH1 = %s\nH2 = %s\nH3 = %s\nH4 = %s\n",
		settings.Jc, settings.Jmin, settings.Jmax, settings.S1, settings.S2, settings.S3, settings.S4,
		settings.H1, settings.H2, settings.H3, settings.H4,
	)
	if strings.TrimSpace(settings.HeaderProtectionKey) == "" {
		return base
	}
	return base + fmt.Sprintf("HeaderProtectionKey = %s\nRandomTrailers = %s\nDisableCookies = %s\n", settings.HeaderProtectionKey, settings.RandomTrailers, settings.DisableCookies)
}

func amneziaWGClientSettings(settings amneziaWGServerSettings) string {
	return canonicalAmneziaWGServerSettings(settings) + "I1 = " + amneziaWG2DefaultI1 + "\n"
}

func amneziaWGSettingsFromGenerated(generated generatedAmneziaWGSettings) (amneziaWGServerSettings, error) {
	return parseAmneziaWGServerSettings(generated.server, nil)
}

func parseAmneziaWGHeaderRange(value string) (uint32, uint32, error) {
	left, right, ok := strings.Cut(strings.TrimSpace(value), "-")
	if !ok {
		return 0, 0, errors.New("header range must use minimum-maximum")
	}
	minimum, err := strconv.ParseUint(strings.TrimSpace(left), 10, 31)
	if err != nil {
		return 0, 0, errors.New("header range minimum is invalid")
	}
	maximum, err := strconv.ParseUint(strings.TrimSpace(right), 10, 31)
	if err != nil {
		return 0, 0, errors.New("header range maximum is invalid")
	}
	if minimum < 5 || minimum >= maximum || maximum >= uint64(amneziaWGHeaderLimit) {
		return 0, 0, fmt.Errorf("header range must satisfy 5 <= minimum < maximum < %d", amneziaWGHeaderLimit)
	}
	return uint32(minimum), uint32(maximum), nil
}

func validateAmneziaWGServerSettings(settings amneziaWGServerSettings) error {
	if settings.Jc < 4 || settings.Jc >= 7 {
		return errors.New("Jc must be between 4 and 6")
	}
	if settings.Jmin < 0 || settings.Jmin > 1280 || settings.Jmax <= settings.Jmin || settings.Jmax > 1280 {
		return errors.New("Jmin and Jmax must satisfy 0 <= Jmin < Jmax <= 1280")
	}
	awg3 := strings.TrimSpace(settings.HeaderProtectionKey) != ""
	if awg3 {
		if settings.S1 < 12 || settings.S1 >= 150 || settings.S2 < 12 || settings.S2 >= 150 || settings.S3 < 12 || settings.S3 >= 64 || settings.S4 < 12 || settings.S4 >= 20 {
			return errors.New("AWG 3.1 requires S1/S2 to be 12-149, S3 to be 12-63, and S4 to be 12-19")
		}
	} else if settings.S1 < 15 || settings.S1 >= 150 || settings.S2 < 15 || settings.S2 >= 150 || settings.S3 < 0 || settings.S3 >= 64 || settings.S4 < 0 || settings.S4 >= 20 {
		return errors.New("S1/S2 must be 15-149, S3 must be 0-63, and S4 must be 0-19")
	}
	values := []int{settings.S1, settings.S2, settings.S3, settings.S4}
	for index, value := range values {
		for _, other := range values[index+1:] {
			if value == other {
				return errors.New("S1, S2, S3, and S4 must be distinct")
			}
		}
	}
	if settings.S1+148 == settings.S2+92 || settings.S1+148 == settings.S3+64 || settings.S2+92 == settings.S3+64 {
		return errors.New("S1, S2, and S3 produce colliding handshake sizes")
	}
	headers := []string{settings.H1, settings.H2, settings.H3, settings.H4}
	previousMaximum := uint32(0)
	for index, header := range headers {
		minimum, maximum, err := parseAmneziaWGHeaderRange(header)
		if err != nil {
			return fmt.Errorf("H%d: %w", index+1, err)
		}
		if index > 0 && minimum <= previousMaximum {
			return errors.New("H1-H4 ranges must be ordered and non-overlapping")
		}
		previousMaximum = maximum
	}
	if !awg3 {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(settings.HeaderProtectionKey))
	if err != nil || len(key) != 32 {
		return errors.New("HeaderProtectionKey must be a 32-byte base64 key")
	}
	if !amneziaWGToggle(settings.RandomTrailers) || !amneziaWGToggle(settings.DisableCookies) {
		return errors.New("RandomTrailers and DisableCookies must be on or off")
	}
	return nil
}

func amneziaWGToggle(value string) bool { return value == "on" || value == "off" }

func parseAmneziaWGServerSettings(content string, defaults *amneziaWGServerSettings) (amneziaWGServerSettings, error) {
	settings := amneziaWGServerSettings{}
	if defaults != nil {
		settings = *defaults
	}
	seen := map[string]bool{}
	for lineNumber, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return amneziaWGServerSettings{}, fmt.Errorf("line %d must use key = value", lineNumber+1)
		}
		validKey := false
		for _, allowed := range amneziaWGSettingOrder {
			if key == allowed {
				validKey = true
				break
			}
		}
		if !validKey {
			return amneziaWGServerSettings{}, fmt.Errorf("line %d contains unsupported server setting %s", lineNumber+1, key)
		}
		if seen[key] {
			return amneziaWGServerSettings{}, fmt.Errorf("line %d duplicates %s", lineNumber+1, key)
		}
		seen[key] = true
		if value == "auto" {
			if defaults == nil {
				return amneziaWGServerSettings{}, fmt.Errorf("line %d cannot use auto in resolved settings", lineNumber+1)
			}
			continue
		}
		if key == "H1" || key == "H2" || key == "H3" || key == "H4" {
			switch key {
			case "H1":
				settings.H1 = value
			case "H2":
				settings.H2 = value
			case "H3":
				settings.H3 = value
			case "H4":
				settings.H4 = value
			}
			continue
		}
		switch key {
		case "HeaderProtectionKey":
			settings.HeaderProtectionKey = value
			continue
		case "RandomTrailers":
			settings.RandomTrailers = value
			continue
		case "DisableCookies":
			settings.DisableCookies = value
			continue
		}
		number, err := strconv.Atoi(value)
		if err != nil {
			return amneziaWGServerSettings{}, fmt.Errorf("line %d requires an integer", lineNumber+1)
		}
		switch key {
		case "Jc":
			settings.Jc = number
		case "Jmin":
			settings.Jmin = number
		case "Jmax":
			settings.Jmax = number
		case "S1":
			settings.S1 = number
		case "S2":
			settings.S2 = number
		case "S3":
			settings.S3 = number
		case "S4":
			settings.S4 = number
		}
	}
	if defaults == nil {
		required := amneziaWGSettingOrder[:11]
		for _, key := range amneziaWG3SettingOrder {
			if seen[key] {
				required = amneziaWGSettingOrder
				break
			}
		}
		for _, key := range required {
			if !seen[key] {
				return amneziaWGServerSettings{}, fmt.Errorf("%s is missing", key)
			}
		}
	}
	if err := validateAmneziaWGServerSettings(settings); err != nil {
		return amneziaWGServerSettings{}, err
	}
	return settings, nil
}

func generatedAmneziaWGServerSettings() (amneziaWGServerSettings, error) {
	generated, err := newAmneziaWG3Settings()
	if err != nil {
		return amneziaWGServerSettings{}, err
	}
	return amneziaWGSettingsFromGenerated(generated)
}

func loadDesiredAmneziaWGServerSettings() (amneziaWGServerSettings, bool, error) {
	body, exists, err := readComponentSettings("amneziawg")
	if err != nil {
		return amneziaWGServerSettings{}, false, err
	}
	if !exists {
		settings, err := generatedAmneziaWGServerSettings()
		return settings, false, err
	}
	settings, err := parseAmneziaWGServerSettings(string(body), nil)
	return settings, true, err
}

func readInstalledAmneziaWGServerSettings() (amneziaWGServerSettings, error) {
	body, err := os.ReadFile(amneziaWGServerPath)
	if err != nil {
		return amneziaWGServerSettings{}, err
	}
	return amneziaWGServerSettingsFromConfiguration(string(body))
}

func amneziaWGServerSettingsFromConfiguration(configuration string) (amneziaWGServerSettings, error) {
	var selected strings.Builder
	for _, raw := range strings.Split(configuration, "\n") {
		key, _, ok := strings.Cut(strings.TrimSpace(raw), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		for _, wanted := range amneziaWGSettingOrder {
			if key == wanted {
				selected.WriteString(strings.TrimSpace(raw))
				selected.WriteByte('\n')
			}
		}
	}
	return parseAmneziaWGServerSettings(selected.String(), nil)
}

func amneziaWGSettingsState() (componentTextSettingsState, error) {
	_, installed := componentOwnership("amneziawg")
	settings, desiredExists, err := loadDesiredAmneziaWGServerSettings()
	if err != nil {
		return componentTextSettingsState{}, err
	}
	if installed {
		current, currentErr := readInstalledAmneziaWGServerSettings()
		if currentErr != nil {
			return componentTextSettingsState{}, fmt.Errorf("read installed AmneziaWG server settings: %w", currentErr)
		}
		settings = current
	}
	content := canonicalAmneziaWGServerSettings(settings)
	if !installed && !desiredExists {
		content = defaultAmneziaWGServerSettingsContent()
	}
	state := componentTextSettingsState{
		ComponentID:    "amneziawg",
		Content:        content,
		DefaultContent: defaultAmneziaWGServerSettingsContent(),
		Installed:      installed,
		Editable:       true,
		Notice:         "These are global desired server settings. They remain available before installation, are used by a later install, and can be saved again after installation.",
		Warning:        "Changing these server obfuscation values requires matching device profiles. SBP refuses a change while any AmneziaWG peer exists.",
	}
	if !installed {
		containers, inspectErr := dockerCommand("ps", "-a", "--format", "{{.Names}}")
		if inspectErr == nil && strings.Contains(strings.ToLower(containers), "amnezia-awg") {
			state.External = true
			state.Warning = "An external AmneziaWG installation is present. Saving records desired SBP settings only and never mutates the external container."
		}
	}
	return state, nil
}

func replaceAmneziaWGServerSettings(configuration string, settings amneziaWGServerSettings) ([]byte, error) {
	if _, err := parseAmneziaWGServerSettings(canonicalAmneziaWGServerSettings(settings), nil); err != nil {
		return nil, err
	}
	lines := strings.Split(configuration, "\n")
	result := make([]string, 0, len(lines)+len(amneziaWGSettingOrder))
	inserted := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		key, _, hasValue := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		isSetting := false
		for _, wanted := range amneziaWGSettingOrder {
			if hasValue && key == wanted {
				isSetting = true
				break
			}
		}
		if isSetting {
			if !inserted {
				result = append(result, strings.Split(strings.TrimSuffix(canonicalAmneziaWGServerSettings(settings), "\n"), "\n")...)
				inserted = true
			}
			continue
		}
		result = append(result, line)
	}
	if !inserted {
		return nil, errors.New("the managed AmneziaWG server settings are missing")
	}
	return []byte(strings.Join(result, "\n")), nil
}

func amneziaWGConfigurationHasPeers(configuration string) bool {
	for _, line := range strings.Split(configuration, "\n") {
		if strings.TrimSpace(line) == "[Peer]" {
			return true
		}
	}
	return false
}

func parseAmneziaWGServerSettingsWithDefaults(content string) (amneziaWGServerSettings, error) {
	var lastErr error
	for range amneziaWGParameterAttempts {
		defaults, err := generatedAmneziaWGServerSettings()
		if err != nil {
			return amneziaWGServerSettings{}, err
		}
		settings, err := parseAmneziaWGServerSettings(content, &defaults)
		if err == nil {
			return settings, nil
		}
		lastErr = err
	}
	return amneziaWGServerSettings{}, lastErr
}

func amneziaWGClientOnlySettings(shared string) string {
	var result strings.Builder
	for _, raw := range strings.Split(shared, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		serverSetting := false
		for _, wanted := range amneziaWGSettingOrder {
			serverSetting = serverSetting || ok && key == wanted
		}
		if !serverSetting {
			result.WriteString(raw)
			result.WriteByte('\n')
		}
	}
	return result.String()
}

func defaultAmneziaWGSettingsApplyOps() amneziaWGSettingsApplyOps {
	const metadataPath = "/opt/vpn-panel-managed/amneziawg/server.json"
	return amneziaWGSettingsApplyOps{
		readServer:   func() ([]byte, error) { return os.ReadFile(amneziaWGServerPath) },
		readMetadata: func() ([]byte, error) { return os.ReadFile(metadataPath) },
		applyServer: func(candidate []byte) error {
			return updateAmneziaWGConfig(amneziaWGServerPath, amneziaWGContainerConfPath, candidate, defaultAmneziaWGRuntimeAPI())
		},
		replaceMetadata: func(candidate []byte) error { return replaceSettingsFile(metadataPath, candidate, 0600) },
	}
}

func applyInstalledAmneziaWGSettings(settings amneziaWGServerSettings, ops amneziaWGSettingsApplyOps) (bool, error) {
	previousConfig, err := ops.readServer()
	if err != nil {
		return false, err
	}
	current, err := amneziaWGServerSettingsFromConfiguration(string(previousConfig))
	if err != nil {
		return false, err
	}
	canonical := canonicalAmneziaWGServerSettings(settings)
	if canonicalAmneziaWGServerSettings(current) == canonical {
		return false, nil
	}
	if amneziaWGConfigurationHasPeers(string(previousConfig)) {
		return false, errors.New("remove all AmneziaWG devices before changing server obfuscation settings")
	}
	previousMetadata, err := ops.readMetadata()
	if err != nil {
		return false, err
	}
	candidate, err := replaceAmneziaWGServerSettings(string(previousConfig), settings)
	if err != nil {
		return false, err
	}
	if err := ops.applyServer(candidate); err != nil {
		return false, err
	}
	rollbackServer := func(applyErr error) error {
		return errors.Join(applyErr, ops.applyServer(previousConfig))
	}
	var metadata map[string]string
	if err := json.Unmarshal(previousMetadata, &metadata); err != nil {
		return false, rollbackServer(errors.New("AmneziaWG server metadata is invalid"))
	}
	clientOnly := amneziaWGClientOnlySettings(metadata["shared"])
	if clientOnly == "" {
		clientOnly = "I1 = " + amneziaWG2DefaultI1 + "\n"
	}
	metadata["shared"] = canonical + clientOnly
	nextMetadata, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return false, rollbackServer(err)
	}
	if err := ops.replaceMetadata(nextMetadata); err != nil {
		return false, rollbackServer(err)
	}
	return true, nil
}

func saveAmneziaWGSettings(content string) (componentTextSettingsState, error) {
	settings, err := parseAmneziaWGServerSettingsWithDefaults(content)
	if err != nil {
		return componentTextSettingsState{}, err
	}
	canonical := []byte(canonicalAmneziaWGServerSettings(settings))
	componentSettingsMu.Lock()
	defer componentSettingsMu.Unlock()
	previousDesired, desiredExisted, err := readComponentSettings("amneziawg")
	if err != nil {
		return componentTextSettingsState{}, err
	}
	if err := writeComponentSettings("amneziawg", canonical); err != nil {
		return componentTextSettingsState{}, err
	}
	if _, installed := componentOwnership("amneziawg"); !installed {
		return amneziaWGSettingsState()
	}
	if _, err := applyInstalledAmneziaWGSettings(settings, defaultAmneziaWGSettingsApplyOps()); err != nil {
		return componentTextSettingsState{}, errors.Join(err, restoreComponentSettings("amneziawg", previousDesired, desiredExisted))
	}
	return amneziaWGSettingsState()
}
