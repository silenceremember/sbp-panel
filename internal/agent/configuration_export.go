package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/silenceremember/sbp-panel/internal/config"
)

func exportComponentConfiguration(c config.Config) (map[string]any, error) {
	settings := map[string]string{}
	installed := []string{}
	for _, id := range []string{"tweaks", "docker", "xray", "xray-xhttp", "amneziawg", "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk"} {
		if _, owned := componentOwnership(id); owned {
			installed = append(installed, id)
		}
		var state componentTextSettingsState
		var err error
		switch id {
		case "tweaks":
			state, err = networkTuningSettingsState()
		case "amneziawg":
			state, err = amneziaWGSettingsState()
		case "xray", "xray-xhttp":
			state, err = xrayTextSettings(id, nil)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		settings[id] = state.Content
	}
	cookies := map[string]json.RawMessage{}
	for _, provider := range []string{"wbstream", "telemost", "dion", "vk"} {
		body, err := os.ReadFile(filepath.Join(c.BypassSecretsDir, provider, "cookies.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(body) > 256<<10 || !json.Valid(body) {
			return nil, errors.New("invalid provider cookies")
		}
		cookies[provider] = body
	}
	return map[string]any{"components": installed, "settings": settings, "cookies": cookies}, nil
}

func validateConfigurationSettings(settings map[string]string) error {
	for id, content := range settings {
		var err error
		switch id {
		case "xray", "xray-xhttp":
			_, err = parseXrayTextSettings(content)
		case "amneziawg":
			_, err = parseAmneziaWGServerSettingsWithDefaults(content)
		case "tweaks":
			_, err = parseNetworkTuningSettings(content)
		default:
			return errors.New("unsupported component settings")
		}
		if err != nil {
			return err
		}
	}
	return nil
}
