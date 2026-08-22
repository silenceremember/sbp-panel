package agent

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func xrayRuntimeUserForID(id string) xrayRuntimeUser {
	return stableXrayVariant.runtimeUser(id)
}

func runtimeTestConfig() map[string]any {
	root := newXrayConfig("private", "0123456789abcdef")
	configureXrayTraffic(root)
	return root
}

func TestApplyXrayRuntimeUserWithoutRestart(t *testing.T) {
	root := runtimeTestConfig()
	users := map[string]bool{}
	api := xrayRuntimeAPI{
		command: func(args ...string) (string, error) {
			switch args[0] {
			case "inbounduser":
				var response struct {
					Users []map[string]string `json:"users"`
				}
				for email := range users {
					response.Users = append(response.Users, map[string]string{"email": email})
				}
				body, _ := json.Marshal(response)
				return string(body), nil
			case "rmu":
				for _, email := range args[3:] {
					delete(users, email)
				}
				return fmt.Sprintf("Removed %d user(s) in total.", len(args)-3), nil
			default:
				t.Fatalf("unexpected command: %#v", args)
				return "", nil
			}
		},
		input: func(input string, args ...string) (string, error) {
			if args[0] != "adu" || args[len(args)-1] != "stdin:" {
				t.Fatalf("unexpected input command: %#v", args)
			}
			var config struct {
				Inbounds []struct {
					Settings struct {
						Clients []struct {
							Email string `json:"email"`
						} `json:"clients"`
					} `json:"settings"`
				} `json:"inbounds"`
			}
			if err := json.Unmarshal([]byte(input), &config); err != nil {
				t.Fatal(err)
			}
			for _, client := range config.Inbounds[0].Settings.Clients {
				users[client.Email] = true
			}
			return fmt.Sprintf("Added %d user(s) in total.", len(config.Inbounds[0].Settings.Clients)), nil
		},
	}
	user := xrayRuntimeUserForID("11111111-2222-4333-8444-555555555555")
	if err := applyXrayRuntimeUsers(api, root, []xrayRuntimeUser{user}, true); err != nil {
		t.Fatal(err)
	}
	if !users[user.Email] {
		t.Fatal("user was not added to the runtime")
	}
	if err := applyXrayRuntimeUsers(api, root, []xrayRuntimeUser{user}, false); err != nil {
		t.Fatal(err)
	}
	if users[user.Email] {
		t.Fatal("user was not removed from the runtime")
	}
}

func TestApplyXrayRuntimeUsersRejectsSilentPartialAdd(t *testing.T) {
	root := runtimeTestConfig()
	api := xrayRuntimeAPI{
		command: func(args ...string) (string, error) {
			return `{"users":[]}`, nil
		},
		input: func(input string, args ...string) (string, error) {
			return "Added 0 user(s) in total.", nil
		},
	}
	err := applyXrayRuntimeUsers(api, root, []xrayRuntimeUser{xrayRuntimeUserForID("11111111-2222-4333-8444-555555555555")}, true)
	if err == nil || !strings.Contains(err.Error(), "unexpected number") {
		t.Fatalf("error = %v", err)
	}
}

func TestPinnedXrayVariantsRunConcurrentlyAndChangeOnlyUsers(t *testing.T) {
	if os.Getenv("CI") != "true" {
		t.Skip("the pinned Xray variant integration test runs in release CI")
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := base64.RawURLEncoding.EncodeToString(private.Bytes())
	stableShortID, xhttpShortID := "0123456789abcdef", "fedcba9876543210"
	tests := []struct {
		variant   xrayVariant
		root      map[string]any
		container string
		user      xrayRuntimeUser
	}{
		{variant: stableXrayVariant, root: newXrayConfig(privateKey, stableShortID), container: fmt.Sprintf("sbp-xray-stable-test-%d", os.Getpid()), user: stableXrayVariant.runtimeUser("11111111-2222-4333-8444-555555555555")},
		{variant: xhttpXrayVariant, root: newXrayConfigFor(xhttpXrayVariant, privateKey, xhttpShortID, "/integration-test-path", map[string]any{"afterBytes": 8388608, "bytesPerSec": 1048576, "burstBytesPerSec": 4194304}), container: fmt.Sprintf("sbp-xray-xhttp-test-%d", os.Getpid()), user: xhttpXrayVariant.runtimeUser("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")},
	}
	for index := range tests {
		test := &tests[index]
		body, err := json.MarshalIndent(test.root, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), test.variant.Method+".json")
		if err := os.WriteFile(path, body, 0644); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("docker", "run", "--rm", "-v", path+":/etc/xray/config.json:ro", xrayImage, "run", "-test", "-config", "/etc/xray/config.json").CombinedOutput(); err != nil {
			t.Fatalf("pinned Xray rejected %s config: %v: %s", test.variant.Method, err, output)
		}
		defer exec.Command("docker", "rm", "-f", test.container).Run()
		if output, err := exec.Command("docker", "run", "-d", "--name", test.container, "-p", "127.0.0.1::8443/tcp", "-v", path+":/etc/xray/config.json:ro", xrayImage, "run", "-config", "/etc/xray/config.json").CombinedOutput(); err != nil {
			t.Fatalf("start pinned %s: %v: %s", test.variant.Method, err, output)
		}
	}
	for index := range tests {
		test := &tests[index]
		api := dockerXrayRuntimeAPI(test.container)
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := xrayRuntimeEmails(api, defaultXrayStatsEndpoint, test.variant.InboundTag); err == nil {
				break
			}
			if time.Now().After(deadline) {
				logs, _ := exec.Command("docker", "logs", test.container).CombinedOutput()
				t.Fatalf("pinned %s HandlerService did not become ready: %s", test.variant.Method, logs)
			}
			time.Sleep(250 * time.Millisecond)
		}
		if err := applyXrayRuntimeUsers(api, test.root, []xrayRuntimeUser{test.user}, true); err != nil {
			t.Fatal(err)
		}
		if _, err := api.command("statsquery", "--server="+defaultXrayStatsEndpoint, "-pattern", "user>>>"); err != nil {
			t.Fatalf("query %s user stats: %v", test.variant.Method, err)
		}
		if err := applyXrayRuntimeUsers(api, test.root, []xrayRuntimeUser{test.user}, false); err != nil {
			t.Fatal(err)
		}
		inspect, err := exec.Command("docker", "inspect", "-f", "{{.RestartCount}}", test.container).CombinedOutput()
		if err != nil || strings.TrimSpace(string(inspect)) != "0" {
			t.Fatalf("%s container restarted while changing a user: restartCount=%q err=%v", test.variant.Method, inspect, err)
		}
	}
}
