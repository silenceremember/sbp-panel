package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"time"
)

const maxXrayRealityServerNames = 32

type xrayRealitySNIState struct {
	DefaultSNI  string   `json:"default_sni"`
	ServerNames []string `json:"server_names"`
	Target      string   `json:"target"`
}

type xrayRealitySNIOps struct {
	owned            func(string) bool
	verifyContainer  func(xrayVariant) error
	validateConfig   func(string) error
	validateTarget   func(string) error
	restartAndVerify func(xrayVariant, map[string]any) error
	captureTraffic   func()
}

func decodeXrayRealityTargetRequest(reader io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(reader, 4<<10+1))
	if err != nil || len(body) == 0 || len(body) > 4<<10 {
		return "", errors.New("invalid REALITY target request size")
	}
	var input struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return "", errors.New("invalid REALITY target request")
	}
	return input.Target, nil
}

func decodeXrayRealitySNIRequest(reader io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(reader, 4<<10+1))
	if err != nil || len(body) == 0 || len(body) > 4<<10 {
		return "", errors.New("invalid SNI request size")
	}
	var input struct {
		SNI string `json:"sni"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		return "", errors.New("invalid SNI request")
	}
	return input.SNI, nil
}

func defaultXrayRealitySNIOps() xrayRealitySNIOps {
	return xrayRealitySNIOps{
		owned: func(method string) bool {
			_, owned := componentOwnership(method)
			return owned
		},
		verifyContainer: verifyManagedXrayContainer,
		validateConfig: func(configPath string) error {
			if _, err := run("docker", "run", "--rm", "-v", configPath+":/etc/xray/config.json:ro", xrayImage, "run", "-test", "-config", "/etc/xray/config.json"); err != nil {
				return fmt.Errorf("the pinned Xray image rejected the staged configuration: %w", err)
			}
			return nil
		},
		validateTarget:   validateXrayRealityTargetReachability,
		restartAndVerify: restartAndVerifyXrayVariant,
		captureTraffic:   captureManagedTraffic,
	}
}

func normalizeXrayRealityTarget(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	host, port, err := net.SplitHostPort(value)
	if err != nil || port != "443" {
		return "", errors.New("REALITY target must use a complete DNS hostname and port 443, such as dl.google.com:443")
	}
	host, err = normalizeXrayRealitySNI(host)
	if err != nil {
		return "", fmt.Errorf("invalid REALITY target hostname: %w", err)
	}
	if net.ParseIP(host) != nil {
		return "", errors.New("REALITY target must use a DNS hostname, not an IP address")
	}
	return net.JoinHostPort(host, "443"), nil
}

func validateXrayRealityTargetReachability(target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "bridge", "--entrypoint", "xray", xrayImage, "tls", "ping", target)
	body, err := command.CombinedOutput()
	if len(body) > 32<<10 {
		body = body[len(body)-(32<<10):]
	}
	output := string(body)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("REALITY target TLS probe timed out after 30 seconds")
	}
	if err != nil {
		return fmt.Errorf("REALITY target TLS probe failed: %w: %s", err, strings.TrimSpace(output))
	}
	if !strings.Contains(output, "Handshake succeeded") || !strings.Contains(output, "TLS ping finished") {
		return errors.New("REALITY target TLS probe did not complete a successful handshake")
	}
	return nil
}

func normalizeXrayRealitySNI(value string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if len(value) < 3 || len(value) > 253 || !strings.Contains(value, ".") {
		return "", errors.New("enter a complete SNI hostname such as dl.google.com")
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("SNI contains an invalid DNS label")
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return "", errors.New("SNI must contain only ASCII letters, numbers, dots, and hyphens; use Punycode for internationalized names")
		}
	}
	return value, nil
}

func managedXrayRealitySettings(root map[string]any, variant xrayVariant) (map[string]any, error) {
	inbound, tag, err := managedXrayInbound(root)
	if err != nil {
		return nil, err
	}
	if tag != variant.InboundTag {
		return nil, fmt.Errorf("the managed Xray inbound tag is %q, want %q", tag, variant.InboundTag)
	}
	stream, _ := inbound["streamSettings"].(map[string]any)
	if stream == nil || strings.TrimSpace(fmt.Sprint(stream["network"])) != variant.Network || strings.TrimSpace(fmt.Sprint(stream["security"])) != "reality" {
		return nil, errors.New("the managed Xray inbound does not have the expected REALITY transport")
	}
	reality, _ := stream["realitySettings"].(map[string]any)
	if reality == nil {
		return nil, errors.New("the managed Xray REALITY settings are missing")
	}
	return reality, nil
}

func xrayRealityServerNames(value any) ([]string, error) {
	var raw []string
	switch names := value.(type) {
	case []string:
		raw = names
	case []any:
		for _, name := range names {
			text, ok := name.(string)
			if !ok {
				return nil, errors.New("the managed Xray serverNames list is invalid")
			}
			raw = append(raw, text)
		}
	default:
		return nil, errors.New("the managed Xray serverNames list is missing")
	}
	if len(raw) == 0 || len(raw) > maxXrayRealityServerNames {
		return nil, errors.New("the managed Xray serverNames list has an invalid size")
	}
	seen := make(map[string]bool, len(raw))
	result := make([]string, 0, len(raw))
	for _, name := range raw {
		normalized, err := normalizeXrayRealitySNI(name)
		if err != nil {
			return nil, fmt.Errorf("invalid managed Xray SNI %q: %w", name, err)
		}
		if seen[normalized] {
			return nil, fmt.Errorf("duplicate managed Xray SNI %q", normalized)
		}
		seen[normalized] = true
		result = append(result, normalized)
	}
	return result, nil
}

func readXrayRealitySNIState(variant xrayVariant) (map[string]any, []byte, xrayRealitySNIState, error) {
	configBody, err := os.ReadFile(variant.ConfigFile)
	if err != nil {
		return nil, nil, xrayRealitySNIState{}, errors.New("the managed Xray configuration is unavailable")
	}
	var root map[string]any
	if err := json.Unmarshal(configBody, &root); err != nil {
		return nil, nil, xrayRealitySNIState{}, errors.New("the managed Xray configuration is invalid")
	}
	reality, err := managedXrayRealitySettings(root, variant)
	if err != nil {
		return nil, nil, xrayRealitySNIState{}, err
	}
	names, err := xrayRealityServerNames(reality["serverNames"])
	if err != nil {
		return nil, nil, xrayRealitySNIState{}, err
	}
	targetValue := reality["target"]
	if variant.Method == stableXrayVariant.Method {
		targetValue = reality["dest"]
	}
	target, err := normalizeXrayRealityTarget(strings.TrimSpace(fmt.Sprint(targetValue)))
	if err != nil {
		return nil, nil, xrayRealitySNIState{}, fmt.Errorf("the managed Xray REALITY target is invalid: %w", err)
	}
	metadata, err := variant.loadClientMetadata()
	if err != nil {
		return nil, nil, xrayRealitySNIState{}, err
	}
	defaultSNI := metadata.SNI
	if strings.TrimSpace(defaultSNI) == "" {
		defaultSNI = xrayRealityServerName
	}
	defaultSNI, err = normalizeXrayRealitySNI(defaultSNI)
	if err != nil {
		return nil, nil, xrayRealitySNIState{}, fmt.Errorf("the default Xray SNI is invalid: %w", err)
	}
	foundDefault := false
	for _, name := range names {
		if name == defaultSNI {
			foundDefault = true
			break
		}
	}
	if !foundDefault {
		return nil, nil, xrayRealitySNIState{}, errors.New("the default Xray SNI is not present in serverNames")
	}
	state := orderedXrayRealitySNIState(defaultSNI, names)
	state.Target = target
	return root, configBody, state, nil
}

func orderedXrayRealitySNIState(defaultSNI string, names []string) xrayRealitySNIState {
	additional := make([]string, 0, len(names)-1)
	for _, name := range names {
		if name != defaultSNI {
			additional = append(additional, name)
		}
	}
	sort.Strings(additional)
	return xrayRealitySNIState{DefaultSNI: defaultSNI, ServerNames: append([]string{defaultSNI}, additional...)}
}

func loadDesiredXrayRealitySNIState(method string) (xrayRealitySNIState, bool, error) {
	body, exists, err := readComponentSettings(method)
	if err != nil {
		return xrayRealitySNIState{}, false, err
	}
	if !exists {
		return xrayRealitySNIState{DefaultSNI: xrayRealityServerName, ServerNames: []string{xrayRealityServerName}, Target: xrayRealityTarget}, false, nil
	}
	var state xrayRealitySNIState
	if err := json.Unmarshal(body, &state); err != nil {
		return xrayRealitySNIState{}, false, errors.New("saved Xray REALITY SNI settings are invalid")
	}
	defaultSNI, err := normalizeXrayRealitySNI(state.DefaultSNI)
	if err != nil || defaultSNI != xrayRealityServerName {
		return xrayRealitySNIState{}, false, errors.New("saved Xray default SNI is invalid")
	}
	names, err := xrayRealityServerNames(state.ServerNames)
	if err != nil {
		return xrayRealitySNIState{}, false, err
	}
	target, err := normalizeXrayRealityTarget(state.Target)
	if err != nil {
		return xrayRealitySNIState{}, false, err
	}
	foundDefault := false
	for _, name := range names {
		foundDefault = foundDefault || name == defaultSNI
	}
	if !foundDefault {
		return xrayRealitySNIState{}, false, errors.New("saved Xray serverNames omit the default SNI")
	}
	ordered := orderedXrayRealitySNIState(defaultSNI, names)
	ordered.Target = target
	return ordered, true, nil
}

func saveDesiredXrayRealitySNIState(method string, state xrayRealitySNIState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeComponentSettings(method, append(body, '\n'))
}

func applyDesiredXrayRealitySNI(root map[string]any, variant xrayVariant) (xrayRealitySNIState, error) {
	state, _, err := loadDesiredXrayRealitySNIState(variant.Method)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	reality, err := managedXrayRealitySettings(root, variant)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	reality["serverNames"] = append([]string(nil), state.ServerNames...)
	if variant.Method == stableXrayVariant.Method {
		reality["dest"] = state.Target
		delete(reality, "target")
	} else {
		reality["target"] = state.Target
		delete(reality, "dest")
	}
	return state, nil
}

func getXrayRealitySNIState(variant xrayVariant) (xrayRealitySNIState, error) {
	xrayConfigMutationMu.Lock()
	defer xrayConfigMutationMu.Unlock()
	ops := defaultXrayRealitySNIOps()
	if !ops.owned(variant.Method) {
		state, _, err := loadDesiredXrayRealitySNIState(variant.Method)
		return state, err
	}
	if err := ops.verifyContainer(variant); err != nil {
		return xrayRealitySNIState{}, err
	}
	_, _, state, err := readXrayRealitySNIState(variant)
	return state, err
}

func addXrayRealitySNI(variant xrayVariant, value string) (xrayRealitySNIState, error) {
	return mutateXrayRealitySNI(variant, value, false, defaultXrayRealitySNIOps())
}

func removeXrayRealitySNI(variant xrayVariant, value string) (xrayRealitySNIState, error) {
	return mutateXrayRealitySNI(variant, value, true, defaultXrayRealitySNIOps())
}

func setXrayRealityTarget(variant xrayVariant, value string) (xrayRealitySNIState, error) {
	return mutateXrayRealityTarget(variant, value, defaultXrayRealitySNIOps())
}

func mutateXrayRealityTarget(variant xrayVariant, value string, ops xrayRealitySNIOps) (xrayRealitySNIState, error) {
	xrayConfigMutationMu.Lock()
	defer xrayConfigMutationMu.Unlock()

	target, err := normalizeXrayRealityTarget(value)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	if !ops.owned(variant.Method) {
		state, _, err := loadDesiredXrayRealitySNIState(variant.Method)
		if err != nil {
			return xrayRealitySNIState{}, err
		}
		state.Target = target
		if err := saveDesiredXrayRealitySNIState(variant.Method, state); err != nil {
			return xrayRealitySNIState{}, err
		}
		return state, nil
	}
	if err := ops.verifyContainer(variant); err != nil {
		return xrayRealitySNIState{}, err
	}
	root, previousBody, previous, err := readXrayRealitySNIState(variant)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	if previous.Target == target {
		return previous, nil
	}
	if err := ops.validateTarget(target); err != nil {
		return xrayRealitySNIState{}, err
	}
	next := previous
	next.Target = target
	return applyXrayRealitySettings(variant, root, previousBody, previous, next, ops)
}

func mutateXrayRealitySNI(variant xrayVariant, value string, remove bool, ops xrayRealitySNIOps) (xrayRealitySNIState, error) {
	xrayConfigMutationMu.Lock()
	defer xrayConfigMutationMu.Unlock()

	name, err := normalizeXrayRealitySNI(value)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	if !ops.owned(variant.Method) {
		previous, _, err := loadDesiredXrayRealitySNIState(variant.Method)
		if err != nil {
			return xrayRealitySNIState{}, err
		}
		next, err := nextXrayRealitySNIState(previous, name, remove)
		if err != nil {
			return xrayRealitySNIState{}, err
		}
		if err := saveDesiredXrayRealitySNIState(variant.Method, next); err != nil {
			return xrayRealitySNIState{}, err
		}
		return next, nil
	}
	if err := ops.verifyContainer(variant); err != nil {
		return xrayRealitySNIState{}, err
	}
	root, previousBody, previous, err := readXrayRealitySNIState(variant)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	nextState, err := nextXrayRealitySNIState(previous, name, remove)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	nextState.Target = previous.Target
	if reflect.DeepEqual(nextState, previous) {
		return previous, nil
	}
	return applyXrayRealitySettings(variant, root, previousBody, previous, nextState, ops)
}

func applyXrayRealitySettings(variant xrayVariant, root map[string]any, previousBody []byte, previous, nextState xrayRealitySNIState, ops xrayRealitySNIOps) (xrayRealitySNIState, error) {
	reality, err := managedXrayRealitySettings(root, variant)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	reality["serverNames"] = nextState.ServerNames
	if variant.Method == stableXrayVariant.Method {
		reality["dest"] = nextState.Target
		delete(reality, "target")
	} else {
		reality["target"] = nextState.Target
		delete(reality, "dest")
	}
	nextBody, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	previousDesired, desiredExisted, err := readComponentSettings(variant.Method)
	if err != nil {
		return xrayRealitySNIState{}, err
	}
	if err := saveDesiredXrayRealitySNIState(variant.Method, nextState); err != nil {
		return xrayRealitySNIState{}, err
	}
	rollbackDesired := func(applyErr error) error {
		return errors.Join(applyErr, restoreComponentSettings(variant.Method, previousDesired, desiredExisted))
	}
	stagedPath := variant.ConfigFile + ".sni-next"
	_ = os.Remove(stagedPath)
	defer os.Remove(stagedPath)
	if err := writeXrayConfig(stagedPath, nextBody); err != nil {
		return xrayRealitySNIState{}, rollbackDesired(fmt.Errorf("stage the updated Xray configuration: %w", err))
	}
	if err := ops.validateConfig(stagedPath); err != nil {
		return xrayRealitySNIState{}, rollbackDesired(err)
	}
	ops.captureTraffic()
	if err := writeXrayConfig(variant.ConfigFile, nextBody); err != nil {
		return xrayRealitySNIState{}, rollbackDesired(fmt.Errorf("save the updated Xray configuration: %w", err))
	}
	if err := ops.restartAndVerify(variant, root); err == nil {
		return nextState, nil
	} else {
		applyErr := err
		if restoreErr := writeXrayConfig(variant.ConfigFile, previousBody); restoreErr != nil {
			return xrayRealitySNIState{}, rollbackDesired(fmt.Errorf("apply the updated Xray SNI list: %w; restoring the previous configuration also failed: %v", applyErr, restoreErr))
		}
		previousRoot := map[string]any{}
		if decodeErr := json.Unmarshal(previousBody, &previousRoot); decodeErr != nil {
			return xrayRealitySNIState{}, rollbackDesired(fmt.Errorf("apply the updated Xray SNI list: %w; the previous configuration could not be decoded for health verification: %v", applyErr, decodeErr))
		}
		if restoreErr := ops.restartAndVerify(variant, previousRoot); restoreErr != nil {
			return xrayRealitySNIState{}, rollbackDesired(fmt.Errorf("apply the updated Xray SNI list: %w; restoring the previous runtime also failed: %v", applyErr, restoreErr))
		}
		return xrayRealitySNIState{}, rollbackDesired(fmt.Errorf("apply the updated Xray SNI list: %w; the previous configuration was restored", applyErr))
	}
}

func nextXrayRealitySNIState(previous xrayRealitySNIState, name string, remove bool) (xrayRealitySNIState, error) {
	present := false
	for _, existing := range previous.ServerNames {
		if existing == name {
			present = true
			break
		}
	}
	if remove {
		if name == previous.DefaultSNI {
			return xrayRealitySNIState{}, errors.New("the default SNI cannot be removed")
		}
		if !present {
			return previous, nil
		}
	} else {
		if present {
			return previous, nil
		}
		if len(previous.ServerNames) >= maxXrayRealityServerNames {
			return xrayRealitySNIState{}, fmt.Errorf("no more than %d SNI values are allowed", maxXrayRealityServerNames)
		}
	}
	names := make([]string, 0, len(previous.ServerNames)+1)
	for _, existing := range previous.ServerNames {
		if !remove || existing != name {
			names = append(names, existing)
		}
	}
	if !remove {
		names = append(names, name)
	}
	next := orderedXrayRealitySNIState(previous.DefaultSNI, names)
	next.Target = previous.Target
	return next, nil
}

func configuredXrayRuntimeEmails(root map[string]any) (map[string]bool, error) {
	inbound, _, err := managedXrayInbound(root)
	if err != nil {
		return nil, err
	}
	settings, _ := inbound["settings"].(map[string]any)
	clients, _ := settings["clients"].([]any)
	want := make(map[string]bool, len(clients))
	for _, raw := range clients {
		client, _ := raw.(map[string]any)
		email := strings.TrimSpace(fmt.Sprint(client["email"]))
		if email == "" || email == "<nil>" {
			return nil, errors.New("the managed Xray configuration contains a client without a traffic identity")
		}
		want[email] = true
	}
	return want, nil
}

func restartAndVerifyXrayVariant(variant xrayVariant, root map[string]any) error {
	endpoint := xrayAPIEndpoint(root)
	if endpoint == "" {
		return errors.New("the managed Xray API endpoint was not found")
	}
	_, tag, err := managedXrayInbound(root)
	if err != nil {
		return err
	}
	wantEmails, err := configuredXrayRuntimeEmails(root)
	if err != nil {
		return err
	}
	if _, err := run("docker", "restart", variant.Container); err != nil {
		return fmt.Errorf("restart %s: %w", variant.Container, err)
	}
	api := dockerXrayRuntimeAPI(variant.Container)
	return waitContainerReady(variant.Container, 15*time.Second, func() error {
		if _, err := run("docker", "exec", variant.Container, "xray", "run", "-test", "-config", "/etc/xray/config.json"); err != nil {
			return err
		}
		actual, err := xrayRuntimeEmails(api, endpoint, tag)
		if err != nil {
			return err
		}
		if len(actual) != len(wantEmails) {
			return fmt.Errorf("Xray loaded %d runtime users, want %d", len(actual), len(wantEmails))
		}
		for email := range wantEmails {
			if !actual[email] {
				return fmt.Errorf("Xray did not restore runtime user %s", email)
			}
		}
		return nil
	})
}
