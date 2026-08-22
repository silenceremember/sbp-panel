package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	amneziaWGContainer         = "amnezia-awg2"
	amneziaWGInterface         = "awg0"
	amneziaWGServerPath        = "/opt/vpn-panel-managed/amneziawg/awg/awg0.conf"
	amneziaWGContainerConfPath = "/opt/amnezia/awg/awg0.conf"
	amneziaWGStagingConfigName = "sbp-awg-next.conf"
)

var amneziaWGCredentialMu sync.Mutex

const amneziaWGCommandTimeout = 20 * time.Second
const amneziaWGCommandLimit = 1 << 20

type amneziaWGCommandOutput struct {
	bytes.Buffer
	overflow bool
}

func (output *amneziaWGCommandOutput) Write(body []byte) (int, error) {
	written := len(body)
	remaining := amneziaWGCommandLimit - output.Len()
	if remaining <= 0 {
		output.overflow = true
		return written, nil
	}
	if len(body) > remaining {
		body = body[:remaining]
		output.overflow = true
	}
	_, _ = output.Buffer.Write(body)
	return written, nil
}

type amneziaWGRuntimeAPI struct {
	strip func(string) (string, error)
	apply func(string) error
	dump  func() (string, error)
}

func defaultAmneziaWGRuntimeAPI() amneziaWGRuntimeAPI {
	return dockerAmneziaWGRuntimeAPI(amneziaWGContainer)
}

func dockerAmneziaWGRuntimeAPI(container string) amneziaWGRuntimeAPI {
	return amneziaWGRuntimeAPI{
		strip: func(path string) (string, error) {
			return runAmneziaWGCommand("", "exec", container, "awg-quick", "strip", path)
		},
		apply: func(configuration string) error {
			_, err := runAmneziaWGCommand(configuration, "exec", "-i", container, "awg", "syncconf", amneziaWGInterface, "/dev/stdin")
			return err
		},
		dump: func() (string, error) {
			return runAmneziaWGCommand("", "exec", container, "awg", "show", amneziaWGInterface, "dump")
		},
	}
}

func runAmneziaWGCommand(input string, args ...string) (string, error) {
	if len(input) > amneziaWGCommandLimit {
		return "", errors.New("AmneziaWG configuration is larger than 1 MiB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), amneziaWGCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", args...)
	if input != "" {
		command.Stdin = strings.NewReader(input)
	}
	var output amneziaWGCommandOutput
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if ctx.Err() != nil {
		return output.String(), fmt.Errorf("docker AmneziaWG command timed out after %s: %w", amneziaWGCommandTimeout, ctx.Err())
	}
	if output.overflow {
		return "", errors.New("docker AmneziaWG command returned more than 1 MiB")
	}
	if err != nil {
		return output.String(), fmt.Errorf("docker: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return strings.TrimSpace(output.String()), nil
}

func amneziaWGConfigPeers(configuration string) (map[string]bool, error) {
	peers := map[string]bool{}
	section := ""
	for _, rawLine := range strings.Split(configuration, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section != "[Peer]" || !strings.HasPrefix(line, "PublicKey =") {
			continue
		}
		publicKey := strings.TrimSpace(strings.TrimPrefix(line, "PublicKey ="))
		if publicKey == "" {
			return nil, errors.New("AmneziaWG peer has no public key")
		}
		if peers[publicKey] {
			return nil, errors.New("AmneziaWG configuration contains a duplicate peer public key")
		}
		peers[publicKey] = true
	}
	return peers, nil
}

func amneziaWGDumpPeers(dump string) (map[string]bool, error) {
	peers := map[string]bool{}
	lines := strings.Split(strings.TrimSpace(dump), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, errors.New("AmneziaWG runtime returned an empty interface dump")
	}
	for _, line := range lines[1:] {
		fields := strings.Split(line, "\t")
		if len(fields) < 8 || strings.TrimSpace(fields[0]) == "" {
			return nil, errors.New("AmneziaWG runtime returned a malformed peer dump")
		}
		publicKey := strings.TrimSpace(fields[0])
		if peers[publicKey] {
			return nil, errors.New("AmneziaWG runtime returned a duplicate peer")
		}
		peers[publicKey] = true
	}
	return peers, nil
}

func sameAmneziaWGPeers(want, actual map[string]bool) bool {
	if len(want) != len(actual) {
		return false
	}
	for publicKey := range want {
		if !actual[publicKey] {
			return false
		}
	}
	return true
}

func applyAmneziaWGRuntime(api amneziaWGRuntimeAPI, strippedConfiguration string) error {
	want, err := amneziaWGConfigPeers(strippedConfiguration)
	if err != nil {
		return err
	}
	if err := api.apply(strippedConfiguration); err != nil {
		return fmt.Errorf("synchronize AmneziaWG runtime peers: %w", err)
	}
	dump, err := api.dump()
	if err != nil {
		return fmt.Errorf("verify AmneziaWG runtime peers: %w", err)
	}
	actual, err := amneziaWGDumpPeers(dump)
	if err != nil {
		return err
	}
	if !sameAmneziaWGPeers(want, actual) {
		return fmt.Errorf("AmneziaWG runtime peer set does not match the persistent configuration: want %d peers, got %d", len(want), len(actual))
	}
	return nil
}

func writeAmneziaWGConfig(path string, body []byte) error {
	if err := os.WriteFile(path, body, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func replaceAmneziaWGConfig(path string, body []byte) error {
	temporary := path + ".next"
	_ = os.Remove(temporary)
	if err := writeAmneziaWGConfig(temporary, body); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		// Production is Linux, where rename replaces the destination atomically.
		// This fallback only keeps repository tests usable on Windows hosts.
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
	return nil
}

func updateAmneziaWGConfig(path, containerPath string, candidate []byte, api amneziaWGRuntimeAPI) error {
	previous, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(previous) > amneziaWGCommandLimit || len(candidate) > amneziaWGCommandLimit {
		return errors.New("AmneziaWG configuration is larger than 1 MiB")
	}
	previousStripped, err := api.strip(containerPath)
	if err != nil {
		return fmt.Errorf("validate current AmneziaWG configuration: %w", err)
	}
	if _, err := amneziaWGConfigPeers(previousStripped); err != nil {
		return fmt.Errorf("validate current AmneziaWG peers: %w", err)
	}

	temporary := filepath.Join(filepath.Dir(path), amneziaWGStagingConfigName)
	containerTemporary := pathpkg.Join(pathpkg.Dir(containerPath), amneziaWGStagingConfigName)
	if err := writeAmneziaWGConfig(temporary, candidate); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	defer os.Remove(temporary)
	candidateStripped, err := api.strip(containerTemporary)
	if err != nil {
		return fmt.Errorf("validate new AmneziaWG configuration: %w", err)
	}
	if _, err := amneziaWGConfigPeers(candidateStripped); err != nil {
		return fmt.Errorf("validate new AmneziaWG peers: %w", err)
	}

	captureManagedTraffic()
	if err := os.Rename(temporary, path); err != nil {
		if runtime.GOOS == "windows" {
			if removeErr := os.Remove(path); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				err = os.Rename(temporary, path)
			}
		}
		if err != nil {
			return err
		}
	}
	if err := applyAmneziaWGRuntime(api, candidateStripped); err == nil {
		return nil
	} else {
		applyErr := err
		if restoreErr := replaceAmneziaWGConfig(path, previous); restoreErr != nil {
			return fmt.Errorf("apply AmneziaWG peer state without restarting the service: %w; restoring the previous persistent configuration also failed: %v", applyErr, restoreErr)
		}
		if restoreErr := applyAmneziaWGRuntime(api, previousStripped); restoreErr != nil {
			return fmt.Errorf("apply AmneziaWG peer state without restarting the service: %w; restoring the previous runtime state also failed: %v", applyErr, restoreErr)
		}
		return fmt.Errorf("apply AmneziaWG peer state without restarting the service: %w", applyErr)
	}
}
