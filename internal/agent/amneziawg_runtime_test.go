package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testAmneziaWGInterface = "private\tpublic\t48692\toff\n"
	testAmneziaWGPeerA     = "peer-a"
	testAmneziaWGPeerB     = "peer-b"
)

func testAmneziaWGConfig(peers ...string) string {
	configuration := "[Interface]\nPrivateKey = private\nListenPort = 48692\n"
	for _, peer := range peers {
		configuration += "\n[Peer]\nPublicKey = " + peer + "\nPresharedKey = shared\nAllowedIPs = 10.8.1.2/32\n"
	}
	return configuration
}

func testAmneziaWGDump(peers ...string) string {
	dump := testAmneziaWGInterface
	for _, peer := range peers {
		dump += peer + "\tshared\tnone\t10.8.1.2/32\t0\t0\t0\t0\n"
	}
	return dump
}

func TestApplyAmneziaWGRuntimeConvergesWithoutRestart(t *testing.T) {
	var runtimePeers []string
	applyCalls := 0
	api := amneziaWGRuntimeAPI{
		apply: func(configuration string) error {
			applyCalls++
			peers, err := amneziaWGConfigPeers(configuration)
			if err != nil {
				return err
			}
			runtimePeers = runtimePeers[:0]
			for _, peer := range []string{testAmneziaWGPeerA, testAmneziaWGPeerB} {
				if peers[peer] {
					runtimePeers = append(runtimePeers, peer)
				}
			}
			return nil
		},
		dump: func() (string, error) { return testAmneziaWGDump(runtimePeers...), nil },
	}
	desired := testAmneziaWGConfig(testAmneziaWGPeerA, testAmneziaWGPeerB)
	if err := applyAmneziaWGRuntime(api, desired); err != nil {
		t.Fatal(err)
	}
	if err := applyAmneziaWGRuntime(api, desired); err != nil {
		t.Fatal(err)
	}
	if applyCalls != 2 || strings.Join(runtimePeers, ",") != testAmneziaWGPeerA+","+testAmneziaWGPeerB {
		t.Fatalf("runtime did not converge idempotently: calls=%d peers=%#v", applyCalls, runtimePeers)
	}
}

func TestApplyAmneziaWGRuntimeRejectsSilentPartialApply(t *testing.T) {
	api := amneziaWGRuntimeAPI{
		apply: func(string) error { return nil },
		dump:  func() (string, error) { return testAmneziaWGDump(testAmneziaWGPeerA), nil },
	}
	err := applyAmneziaWGRuntime(api, testAmneziaWGConfig(testAmneziaWGPeerA, testAmneziaWGPeerB))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestAmneziaWGCommandOutputIsBounded(t *testing.T) {
	var output amneziaWGCommandOutput
	body := []byte(strings.Repeat("x", amneziaWGCommandLimit+128))
	written, err := output.Write(body)
	if err != nil || written != len(body) || output.Len() != amneziaWGCommandLimit || !output.overflow {
		t.Fatalf("bounded output = written %d, len %d, overflow %v, err %v", written, output.Len(), output.overflow, err)
	}
}

func TestUpdateAmneziaWGConfigRejectsOversizedCandidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg0.conf")
	previous := testAmneziaWGConfig(testAmneziaWGPeerA)
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	stripCalls := 0
	api := amneziaWGRuntimeAPI{strip: func(string) (string, error) {
		stripCalls++
		return previous, nil
	}}
	err := updateAmneziaWGConfig(path, "/container/awg0.conf", []byte(strings.Repeat("x", amneziaWGCommandLimit+1)), api)
	if err == nil || !strings.Contains(err.Error(), "larger than 1 MiB") || stripCalls != 0 {
		t.Fatalf("error = %v, strip calls = %d", err, stripCalls)
	}
}

func TestUpdateAmneziaWGConfigRestoresFileAndRuntime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg0.conf")
	previous := testAmneziaWGConfig(testAmneziaWGPeerA)
	candidate := testAmneziaWGConfig(testAmneziaWGPeerA, testAmneziaWGPeerB)
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	var runtimePeers []string
	applyCalls := 0
	api := amneziaWGRuntimeAPI{
		strip: func(runtimePath string) (string, error) {
			if filepath.Base(runtimePath) == amneziaWGStagingConfigName {
				return candidate, nil
			}
			return previous, nil
		},
		apply: func(configuration string) error {
			applyCalls++
			if strings.Contains(configuration, testAmneziaWGPeerB) {
				runtimePeers = []string{testAmneziaWGPeerA, testAmneziaWGPeerB}
				return errors.New("injected live apply failure")
			}
			runtimePeers = []string{testAmneziaWGPeerA}
			return nil
		},
		dump: func() (string, error) { return testAmneziaWGDump(runtimePeers...), nil },
	}
	err := updateAmneziaWGConfig(path, "/container/awg0.conf", []byte(candidate), api)
	if err == nil || !strings.Contains(err.Error(), "injected live apply failure") {
		t.Fatalf("error = %v", err)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil || string(body) != previous {
		t.Fatalf("persistent configuration was not restored: %v, %q", readErr, body)
	}
	if applyCalls != 2 || strings.Join(runtimePeers, ",") != testAmneziaWGPeerA {
		t.Fatalf("runtime was not restored: calls=%d peers=%#v", applyCalls, runtimePeers)
	}
	if _, statErr := os.Stat(filepath.Join(dir, amneziaWGStagingConfigName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staging file was left behind: %v", statErr)
	}
}

func TestUpdateAmneziaWGConfigReportsRuntimeRecoveryFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg0.conf")
	previous := testAmneziaWGConfig(testAmneziaWGPeerA)
	candidate := testAmneziaWGConfig(testAmneziaWGPeerB)
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	api := amneziaWGRuntimeAPI{
		strip: func(runtimePath string) (string, error) {
			if filepath.Base(runtimePath) == amneziaWGStagingConfigName {
				return candidate, nil
			}
			return previous, nil
		},
		apply: func(configuration string) error {
			if strings.Contains(configuration, testAmneziaWGPeerB) {
				return errors.New("primary failure")
			}
			return errors.New("recovery failure")
		},
		dump: func() (string, error) { return testAmneziaWGDump(), nil },
	}
	err := updateAmneziaWGConfig(path, "/container/awg0.conf", []byte(candidate), api)
	if err == nil || !strings.Contains(err.Error(), "primary failure") || !strings.Contains(err.Error(), "recovery failure") {
		t.Fatalf("error = %v", err)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil || string(body) != previous {
		t.Fatalf("persistent configuration was not restored: %v, %q", readErr, body)
	}
}

func TestUpdateAmneziaWGConfigRejectsInvalidCandidateBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg0.conf")
	previous := testAmneziaWGConfig(testAmneziaWGPeerA)
	candidate := testAmneziaWGConfig(testAmneziaWGPeerB, testAmneziaWGPeerB)
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	applyCalls := 0
	api := amneziaWGRuntimeAPI{
		strip: func(runtimePath string) (string, error) {
			if filepath.Base(runtimePath) == amneziaWGStagingConfigName {
				return candidate, nil
			}
			return previous, nil
		},
		apply: func(string) error { applyCalls++; return nil },
		dump:  func() (string, error) { return testAmneziaWGDump(testAmneziaWGPeerA), nil },
	}
	err := updateAmneziaWGConfig(path, "/container/awg0.conf", []byte(candidate), api)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v", err)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil || string(body) != previous || applyCalls != 0 {
		t.Fatalf("invalid candidate mutated state: readErr=%v body=%q calls=%d", readErr, body, applyCalls)
	}
}

func TestAmneziaWGClientParametersUseMetadataBeforeProvisioning(t *testing.T) {
	dir := t.TempDir()
	metadata := `{"server_public":"server-public","endpoint":"192.0.2.1:48692","shared":"Jc = 5\nH1 = 1001\n"}`
	if err := os.WriteFile(filepath.Join(dir, "server.json"), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	public, endpoint, shared, err := amneziaWGClientParameters(dir)
	if err != nil {
		t.Fatal(err)
	}
	if public != "server-public" || endpoint != "192.0.2.1:48692" || !strings.Contains(shared, "Jc = 5") {
		t.Fatalf("parameters = %q, %q, %q", public, endpoint, shared)
	}
}

func TestAmneziaWGClientParametersFailBeforePeerMutation(t *testing.T) {
	_, _, _, err := amneziaWGClientParameters(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func dockerAmneziaWGTestCommand(t *testing.T, input string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("docker %s timed out: %v", strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		t.Fatalf("docker %s: %v: stdout=%s stderr=%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func TestPinnedAmneziaWGSyncConfChangesPeersWithoutRestart(t *testing.T) {
	if os.Getenv("CI") != "true" {
		t.Skip("the pinned AmneziaWG syncconf integration test runs in release CI")
	}
	serverPrivate := dockerAmneziaWGTestCommand(t, "", "run", "--rm", "--entrypoint", "awg", awgBaseImage, "genkey")
	peerAPrivate := dockerAmneziaWGTestCommand(t, "", "run", "--rm", "--entrypoint", "awg", awgBaseImage, "genkey")
	peerAPublic := dockerAmneziaWGTestCommand(t, peerAPrivate+"\n", "run", "--rm", "-i", "--entrypoint", "awg", awgBaseImage, "pubkey")
	peerAPSK := dockerAmneziaWGTestCommand(t, "", "run", "--rm", "--entrypoint", "awg", awgBaseImage, "genpsk")
	peerBPrivate := dockerAmneziaWGTestCommand(t, "", "run", "--rm", "--entrypoint", "awg", awgBaseImage, "genkey")
	peerBPublic := dockerAmneziaWGTestCommand(t, peerBPrivate+"\n", "run", "--rm", "-i", "--entrypoint", "awg", awgBaseImage, "pubkey")
	peerBPSK := dockerAmneziaWGTestCommand(t, "", "run", "--rm", "--entrypoint", "awg", awgBaseImage, "genpsk")

	interfaceConfig := fmt.Sprintf("[Interface]\nPrivateKey = %s\nListenPort = 48692\nJc = 5\nJmin = 50\nJmax = 1000\nS1 = 75\nS2 = 150\nH1 = 1001\nH2 = 1002\nH3 = 1003\nH4 = 1004\n", serverPrivate)
	peerAConfig := fmt.Sprintf("\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = 10.8.1.2/32\n", peerAPublic, peerAPSK)
	peerBConfig := fmt.Sprintf("\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = 10.8.1.3/32\n", peerBPublic, peerBPSK)
	dir := t.TempDir()
	path := filepath.Join(dir, "awg0.conf")
	if err := os.WriteFile(path, []byte(interfaceConfig+peerAConfig), 0600); err != nil {
		t.Fatal(err)
	}

	container := fmt.Sprintf("sbp-awg-syncconf-test-%d", os.Getpid())
	defer exec.Command("docker", "rm", "-f", container).Run()
	volume := dir + ":/config"
	dockerAmneziaWGTestCommand(t, "", "run", "-d", "--name", container, "--privileged", "--log-driver", "local", "--log-opt", "max-size=1m", "--log-opt", "max-file=1", "--log-opt", "compress=false", "-v", volume, awgBaseImage, "bash", "-c", "set -e; awg-quick up /config/awg0.conf; exec tail -f /dev/null")
	api := dockerAmneziaWGRuntimeAPI(container)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if dump, err := api.dump(); err == nil {
			peers, parseErr := amneziaWGDumpPeers(dump)
			if parseErr == nil && peers[peerAPublic] {
				break
			}
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
			inspect, _ := exec.Command("docker", "inspect", container).CombinedOutput()
			t.Fatalf("pinned AmneziaWG interface did not become ready:\nlogs:\n%s\ninspect:\n%s", logs, inspect)
		}
		time.Sleep(250 * time.Millisecond)
	}

	if err := os.WriteFile(path, []byte(interfaceConfig+peerAConfig+peerBConfig), 0600); err != nil {
		t.Fatal(err)
	}
	stripped, err := api.strip("/config/awg0.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyAmneziaWGRuntime(api, stripped); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(interfaceConfig+peerAConfig), 0600); err != nil {
		t.Fatal(err)
	}
	stripped, err = api.strip("/config/awg0.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyAmneziaWGRuntime(api, stripped); err != nil {
		t.Fatal(err)
	}
	inspect, err := exec.Command("docker", "inspect", "-f", "{{.RestartCount}}", container).CombinedOutput()
	if err != nil || strings.TrimSpace(string(inspect)) != "0" {
		t.Fatalf("AmneziaWG container restarted while changing peers: restartCount=%q err=%v", inspect, err)
	}
}
