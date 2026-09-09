package agent

import (
	"encoding/json"
	"errors"
	"strings"
)

func xrayTextSettings(method string, content *string) (componentTextSettingsState, error) {
	variant, _ := xrayVariantForMethod(method)
	if content != nil {
		requested, err := parseXrayTextSettings(*content)
		if err != nil {
			return componentTextSettingsState{}, err
		}
		if _, err := replaceXrayRealitySettings(variant, requested.Target, requested.ServerNames, requested.Fingerprint); err != nil {
			return componentTextSettingsState{}, err
		}
	}
	state, err := getXrayRealitySNIState(variant)
	if err != nil {
		return componentTextSettingsState{}, err
	}
	if state.Fingerprint == "" {
		state.Fingerprint = "chrome"
	}
	defaults := xrayRealitySNIState{DefaultSNI: xrayRealityServerName, ServerNames: []string{xrayRealityServerName}, Target: xrayRealityTarget, Fingerprint: "chrome"}
	currentJSON, _ := json.MarshalIndent(state, "", "  ")
	defaultJSON, _ := json.MarshalIndent(defaults, "", "  ")
	_, installed := componentOwnership(method)
	return componentTextSettingsState{ComponentID: method, Content: string(currentJSON), DefaultContent: string(defaultJSON), Installed: installed, Editable: true,
		Notice: "REALITY target and accepted server names are server settings. Default SNI and fingerprint affect newly generated profiles. Fingerprint can also be changed in the client. Refresh existing profiles after changing the default SNI."}, nil
}

func parseXrayTextSettings(content string) (xrayRealitySNIState, error) {
	if !json.Valid([]byte(content)) {
		return xrayRealitySNIState{}, errors.New("invalid settings JSON")
	}
	var requested xrayRealitySNIState
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&requested); err != nil {
		return xrayRealitySNIState{}, err
	}
	if requested.Fingerprint == "" {
		requested.Fingerprint = "chrome"
	}
	switch requested.Fingerprint {
	case "chrome", "firefox", "safari", "ios", "android", "edge", "random", "randomized":
	default:
		return xrayRealitySNIState{}, errors.New("unsupported fingerprint")
	}
	names := []string{requested.DefaultSNI}
	for _, name := range requested.ServerNames {
		if name != requested.DefaultSNI {
			names = append(names, name)
		}
	}
	state, err := normalizeRequestedXrayRealitySettings(requested.Target, names)
	state.Fingerprint = requested.Fingerprint
	return state, err
}
