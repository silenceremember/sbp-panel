package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/silenceremember/sbp-panel/internal/config"
)

const (
	amneziaWGUpdateImage         = "vpn-panel-amneziawg:next"
	amneziaWGUpdateRollbackImage = "vpn-panel-amneziawg:rollback"
	amneziaWGUpdateBuildDir      = "/opt/vpn-panel-managed/amneziawg/.component-update-build"
)

var amneziaWGUpdateSnapshotPath = "/opt/vpn-panel-managed/amneziawg/.component-update-rollback.json"

type amneziaWGComponentDevice struct {
	DeviceID int64  `json:"device_id"`
	Name     string `json:"name"`
	Active   bool   `json:"active"`
}

type amneziaWGComponentProfile struct {
	DeviceID          int64  `json:"device_id"`
	Credential        string `json:"credential"`
	ProfileGeneration int    `json:"profile_generation"`
	ProtocolVersion   string `json:"protocol_version"`
}

type amneziaWGComponentUpdateResult struct {
	Token    string                      `json:"token"`
	Devices  []amneziaWGComponentDevice  `json:"devices"`
	Profiles []amneziaWGComponentProfile `json:"profiles"`
}

type amneziaWGComponentUpdateSnapshot struct {
	Token            string                      `json:"token"`
	Devices          []amneziaWGComponentDevice  `json:"devices"`
	Profiles         []amneziaWGComponentProfile `json:"profiles"`
	PreviousConfig   []byte                      `json:"previous_config"`
	PreviousMetadata []byte                      `json:"previous_metadata"`
	PreviousDesired  []byte                      `json:"previous_desired"`
	DesiredExisted   bool                        `json:"desired_existed"`
	PreviousImageID  string                      `json:"previous_image_id"`
	CandidateImageID string                      `json:"candidate_image_id"`
}

func amneziaWGUpdateToken() (string, error) {
	body := make([]byte, 16)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return hex.EncodeToString(body), nil
}

func amneziaWGImageKey(image, command string) (string, error) {
	switch command {
	case "genkey", "genpsk":
		value, err := run("docker", "run", "--rm", "--entrypoint", "awg", image, command)
		return strings.TrimSpace(value), err
	default:
		return "", errors.New("unsupported AmneziaWG key operation")
	}
}

func amneziaWGImagePublicKey(image, private string) (string, error) {
	value, err := runInput(strings.TrimSpace(private)+"\n", "docker", "run", "--rm", "-i", "--entrypoint", "awg", image, "pubkey")
	return strings.TrimSpace(value), err
}

var amneziaWGUpdateKey = amneziaWGImageKey
var amneziaWGUpdatePublicKey = amneziaWGImagePublicKey

func validateAmneziaWGComponentDevices(devices []amneziaWGComponentDevice) error {
	if len(devices) > 253 {
		return errors.New("AmneziaWG supports at most 253 managed devices")
	}
	seen := make(map[int64]bool, len(devices))
	for _, device := range devices {
		if device.DeviceID <= 0 || seen[device.DeviceID] {
			return errors.New("AmneziaWG update contains an invalid or duplicate device ID")
		}
		seen[device.DeviceID] = true
		if strings.TrimSpace(device.Name) == "" || strings.ContainsAny(device.Name, "\r\n") {
			return errors.New("AmneziaWG update contains an invalid device name")
		}
	}
	return nil
}

func generateAmneziaWG3Deployment(image string, devices []amneziaWGComponentDevice) ([]byte, []byte, []byte, []amneziaWGComponentProfile, error) {
	if err := validateAmneziaWGComponentDevices(devices); err != nil {
		return nil, nil, nil, nil, err
	}
	settings, err := generatedAmneziaWGServerSettings()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	serverPrivate, err := amneziaWGUpdateKey(image, "genkey")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	serverPublic, err := amneziaWGUpdatePublicKey(image, serverPrivate)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	shared := amneziaWGClientSettings(settings)
	endpoint := fmt.Sprintf("%s:%d", publicServerAddress(), awgPort)
	server := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.8.1.1/24\nListenPort = %d\n%s", serverPrivate, awgPort, canonicalAmneziaWGServerSettings(settings))
	profiles := make([]amneziaWGComponentProfile, 0, len(devices))
	for index, device := range devices {
		clientPrivate, err := amneziaWGUpdateKey(image, "genkey")
		if err != nil {
			return nil, nil, nil, nil, err
		}
		clientPublic, err := amneziaWGUpdatePublicKey(image, clientPrivate)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		psk, err := amneziaWGUpdateKey(image, "genpsk")
		if err != nil {
			return nil, nil, nil, nil, err
		}
		address := fmt.Sprintf("10.8.1.%d/32", index+2)
		if device.Active {
			server += fmt.Sprintf("\n# %s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s\n", device.Name, clientPublic, psk, address)
		}
		credential := fmt.Sprintf("[Interface]\nAddress = %s\nDNS = 1.1.1.1, 1.0.0.1\nMTU = %d\nPrivateKey = %s\n%s\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = %s\nPersistentKeepalive = 25\n", address, amneziaWGClientMTU, clientPrivate, shared, serverPublic, psk, endpoint)
		profiles = append(profiles, amneziaWGComponentProfile{DeviceID: device.DeviceID, Credential: credential, ProfileGeneration: amneziaWGProfileGeneration, ProtocolVersion: "3.1"})
	}
	metadata, err := json.MarshalIndent(map[string]string{
		"server_public": serverPublic,
		"endpoint":      endpoint,
		"shared":        shared,
		"protocol":      "3.1",
	}, "", "  ")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return []byte(server), metadata, []byte(canonicalAmneziaWGServerSettings(settings)), profiles, nil
}

func readAmneziaWGUpdateSnapshot() (amneziaWGComponentUpdateSnapshot, error) {
	body, err := os.ReadFile(amneziaWGUpdateSnapshotPath)
	if err != nil {
		return amneziaWGComponentUpdateSnapshot{}, err
	}
	if len(body) > amneziaWGCommandLimit {
		return amneziaWGComponentUpdateSnapshot{}, errors.New("AmneziaWG rollback snapshot is too large")
	}
	var snapshot amneziaWGComponentUpdateSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil || snapshot.Token == "" {
		return amneziaWGComponentUpdateSnapshot{}, errors.New("AmneziaWG rollback snapshot is invalid")
	}
	return snapshot, nil
}

func amneziaWGComponentUpdatePending() bool {
	_, err := os.Stat(amneziaWGUpdateSnapshotPath)
	return err == nil
}

func ensureAmneziaWGComponentUpdateIdle() error {
	if amneziaWGComponentUpdatePending() {
		return errors.New("AmneziaWG component update is awaiting profile publication")
	}
	return nil
}

func writeAmneziaWGUpdateSnapshot(snapshot amneziaWGComponentUpdateSnapshot) error {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(body) > amneziaWGCommandLimit {
		return errors.New("AmneziaWG rollback snapshot is too large")
	}
	return replaceSettingsFile(amneziaWGUpdateSnapshotPath, body, 0600)
}

func verifyAmneziaWG3Candidate(image string, configuration []byte) error {
	path := filepath.Join(amneziaWGUpdateBuildDir, "awg0.conf")
	if err := os.WriteFile(path, configuration, 0600); err != nil {
		return err
	}
	defer os.Remove(path)
	if _, err := run("docker", "run", "--rm", "--entrypoint", "awg-quick", "-v", path+":/tmp/awg0.conf:ro", image, "strip", "/tmp/awg0.conf"); err != nil {
		return fmt.Errorf("validate generated AmneziaWG 3.1 configuration: %w", err)
	}
	return nil
}

func prepareAmneziaWGComponentUpdate(devices []amneziaWGComponentDevice) (result amneziaWGComponentUpdateResult, err error) {
	amneziaWGCredentialMu.Lock()
	defer amneziaWGCredentialMu.Unlock()
	if _, owned := componentOwnership("amneziawg"); !owned {
		return result, errors.New("AmneziaWG is not managed by SBP")
	}
	previousConfig, err := os.ReadFile(amneziaWGServerPath)
	if err != nil {
		return result, err
	}
	if strings.Contains(string(previousConfig), "HeaderProtectionKey =") {
		return result, errors.New("AmneziaWG protocol 3.1 is already installed")
	}
	if _, err := os.Stat(amneziaWGUpdateSnapshotPath); err == nil {
		return result, errors.New("an AmneziaWG component update is already awaiting completion")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if imageExists(amneziaWGUpdateImage) {
		return result, errors.New("the reserved AmneziaWG update image already exists")
	}
	if err := os.MkdirAll(amneziaWGUpdateBuildDir, 0700); err != nil {
		return result, err
	}
	defer os.RemoveAll(amneziaWGUpdateBuildDir)
	if err := writeAmneziaWGBuildContext(amneziaWGUpdateBuildDir); err != nil {
		return result, err
	}
	if _, err := run("docker", "build", "--pull", "--force-rm", "-t", amneziaWGUpdateImage, amneziaWGUpdateBuildDir); err != nil {
		return result, err
	}
	candidateImageID, err := dockerCommand("image", "inspect", "--format", "{{.Id}}", amneziaWGUpdateImage)
	if err != nil || strings.TrimSpace(candidateImageID) == "" {
		return result, errors.New("inspect the candidate AmneziaWG image")
	}
	keepCandidate := false
	defer func() {
		if !keepCandidate && !amneziaWGComponentUpdatePending() {
			_ = removeImagesStrict(amneziaWGUpdateImage)
		}
	}()
	candidateConfig, candidateMetadata, candidateDesired, profiles, err := generateAmneziaWG3Deployment(amneziaWGUpdateImage, devices)
	if err != nil {
		return result, err
	}
	if err := verifyAmneziaWG3Candidate(amneziaWGUpdateImage, candidateConfig); err != nil {
		return result, err
	}
	metadataPath := "/opt/vpn-panel-managed/amneziawg/server.json"
	previousMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		return result, err
	}
	previousDesired, desiredExisted, err := readComponentSettings("amneziawg")
	if err != nil {
		return result, err
	}
	previousImageID, err := dockerCommand("image", "inspect", "--format", "{{.Id}}", "vpn-panel-amneziawg:locked")
	if err != nil || strings.TrimSpace(previousImageID) == "" {
		return result, errors.New("inspect the current managed AmneziaWG image before update")
	}
	token, err := amneziaWGUpdateToken()
	if err != nil {
		return result, err
	}
	snapshot := amneziaWGComponentUpdateSnapshot{
		Token: token, Devices: devices, Profiles: profiles, PreviousConfig: previousConfig, PreviousMetadata: previousMetadata,
		PreviousDesired: previousDesired, DesiredExisted: desiredExisted, PreviousImageID: strings.TrimSpace(previousImageID), CandidateImageID: strings.TrimSpace(candidateImageID),
	}
	if err := writeAmneziaWGUpdateSnapshot(snapshot); err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, rollbackAmneziaWGComponentUpdateLocked(token))
		}
	}()
	captureManagedTraffic()
	if err = removeContainersStrict(amneziaWGContainer); err != nil {
		return result, err
	}
	if err = replaceAmneziaWGConfig(amneziaWGServerPath, candidateConfig); err != nil {
		return result, err
	}
	if err = replaceSettingsFile(metadataPath, candidateMetadata, 0600); err != nil {
		return result, err
	}
	if err = writeComponentSettings("amneziawg", candidateDesired); err != nil {
		return result, err
	}
	if _, err = run("docker", amneziaWGContainerArgsFor(amneziaWGUpdateImage)...); err != nil {
		return result, err
	}
	if err = waitContainerReady(amneziaWGContainer, 15*time.Second, func() error {
		_, showErr := run("docker", "exec", amneziaWGContainer, "awg", "show", amneziaWGInterface)
		return showErr
	}); err != nil {
		return result, err
	}
	keepCandidate = true
	return amneziaWGComponentUpdateResult{Token: token, Devices: devices, Profiles: profiles}, nil
}

func rollbackAmneziaWGComponentUpdateLocked(token string) error {
	snapshot, err := readAmneziaWGUpdateSnapshot()
	if err != nil {
		return err
	}
	if token == "" || token != snapshot.Token {
		return errors.New("AmneziaWG rollback token does not match")
	}
	var failures []error
	if err := removeContainersStrict(amneziaWGContainer); err != nil {
		failures = append(failures, err)
	}
	if err := replaceAmneziaWGConfig(amneziaWGServerPath, snapshot.PreviousConfig); err != nil {
		failures = append(failures, err)
	}
	if err := replaceSettingsFile("/opt/vpn-panel-managed/amneziawg/server.json", snapshot.PreviousMetadata, 0600); err != nil {
		failures = append(failures, err)
	}
	if err := restoreComponentSettings("amneziawg", snapshot.PreviousDesired, snapshot.DesiredExisted); err != nil {
		failures = append(failures, err)
	}
	if _, err := run("docker", "image", "tag", snapshot.PreviousImageID, "vpn-panel-amneziawg:locked"); err != nil {
		failures = append(failures, fmt.Errorf("restore the previous managed AmneziaWG image: %w", err))
	}
	if _, err := run("docker", amneziaWGContainerArgs()...); err != nil {
		failures = append(failures, err)
	} else if err := waitContainerReady(amneziaWGContainer, 15*time.Second, func() error {
		_, showErr := run("docker", "exec", amneziaWGContainer, "awg", "show", amneziaWGInterface)
		return showErr
	}); err != nil {
		failures = append(failures, err)
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if err := removeImagesStrict(amneziaWGUpdateImage, amneziaWGUpdateRollbackImage); err != nil {
		return err
	}
	if snapshot.CandidateImageID != "" && snapshot.CandidateImageID != snapshot.PreviousImageID {
		if err := removeImagesStrict(snapshot.CandidateImageID); err != nil {
			return err
		}
	}
	return os.Remove(amneziaWGUpdateSnapshotPath)
}

func rollbackAmneziaWGComponentUpdate(token string) error {
	amneziaWGCredentialMu.Lock()
	defer amneziaWGCredentialMu.Unlock()
	return rollbackAmneziaWGComponentUpdateLocked(token)
}

func commitAmneziaWGComponentUpdate(token string) error {
	amneziaWGCredentialMu.Lock()
	defer amneziaWGCredentialMu.Unlock()
	snapshot, err := readAmneziaWGUpdateSnapshot()
	if err != nil {
		return err
	}
	if token == "" || token != snapshot.Token {
		return errors.New("AmneziaWG commit token does not match")
	}
	if _, err := run("docker", "image", "tag", snapshot.PreviousImageID, amneziaWGUpdateRollbackImage); err != nil {
		return fmt.Errorf("preserve the previous AmneziaWG image for commit rollback: %w", err)
	}
	if _, err := run("docker", "image", "tag", amneziaWGUpdateImage, "vpn-panel-amneziawg:locked"); err != nil {
		return err
	}
	if err := removeImagesStrict(amneziaWGUpdateImage); err != nil {
		return err
	}
	if err := removeImagesStrict(amneziaWGUpdateRollbackImage); err != nil {
		return err
	}
	return os.Remove(amneziaWGUpdateSnapshotPath)
}

func currentAmneziaWGComponentUpdateResult() (amneziaWGComponentUpdateResult, error) {
	snapshot, err := readAmneziaWGUpdateSnapshot()
	if err != nil {
		return amneziaWGComponentUpdateResult{}, err
	}
	return amneziaWGComponentUpdateResult{Token: snapshot.Token, Devices: snapshot.Devices, Profiles: snapshot.Profiles}, nil
}

func (i *installer) startAmneziaWGUpdate(devices []amneziaWGComponentDevice) error {
	return i.startJob("amneziawg", "update", func(string, config.Config) (string, error) {
		result, err := prepareAmneziaWGComponentUpdate(devices)
		if err != nil {
			return "", err
		}
		if result.Token == "" || len(result.Profiles) != len(devices) {
			return "", errors.New("AmneziaWG update produced an incomplete result")
		}
		return "AmneziaWG 3.1 is ready for profile publication", nil
	})
}

func (i *installer) finishAmneziaWGUpdate(status, output string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.jobs["amneziawg"] = installJob{ComponentID: "amneziawg", Operation: "update", Status: status, Output: output}
}
