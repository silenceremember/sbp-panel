package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestGenerateAmneziaWG3DeploymentReissuesAllProfilesAndOnlyActivatesDesiredPeers(t *testing.T) {
	oldKey, oldPublic := amneziaWGUpdateKey, amneziaWGUpdatePublicKey
	t.Cleanup(func() {
		amneziaWGUpdateKey, amneziaWGUpdatePublicKey = oldKey, oldPublic
	})
	sequence := 0
	amneziaWGUpdateKey = func(_ string, command string) (string, error) {
		sequence++
		return fmt.Sprintf("%s-%d", command, sequence), nil
	}
	amneziaWGUpdatePublicKey = func(_ string, private string) (string, error) {
		return "public-" + private, nil
	}
	server, metadata, desired, profiles, err := generateAmneziaWG3Deployment("candidate", []amneziaWGComponentDevice{
		{DeviceID: 11, Name: "Active phone", Active: true},
		{DeviceID: 12, Name: "Suspended tablet", Active: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].DeviceID != 11 || profiles[1].DeviceID != 12 {
		t.Fatalf("profiles=%#v", profiles)
	}
	for index, profile := range profiles {
		if profile.ProfileGeneration != 2 || profile.ProtocolVersion != "3.1" || !strings.Contains(profile.Credential, "MTU = 1376") || !strings.Contains(profile.Credential, "HeaderProtectionKey =") {
			t.Fatalf("profile %d is incomplete: %#v", index, profile)
		}
	}
	serverText := string(server)
	if !strings.Contains(serverText, "# Active phone") || strings.Contains(serverText, "# Suspended tablet") || strings.Count(serverText, "[Peer]") != 1 {
		t.Fatalf("active peer set=%q", serverText)
	}
	if !strings.Contains(string(desired), "RandomTrailers = off") || !strings.Contains(string(desired), "DisableCookies = off") {
		t.Fatalf("desired settings=%q", desired)
	}
	var values map[string]string
	if err := json.Unmarshal(metadata, &values); err != nil {
		t.Fatal(err)
	}
	if values["protocol"] != "3.1" || !strings.Contains(values["shared"], "I1 = ") {
		t.Fatalf("metadata=%#v", values)
	}
}

func TestGenerateAmneziaWG3DeploymentRejectsDuplicateDevices(t *testing.T) {
	_, _, _, _, err := generateAmneziaWG3Deployment("candidate", []amneziaWGComponentDevice{
		{DeviceID: 5, Name: "One"},
		{DeviceID: 5, Name: "Two"},
	})
	if err == nil {
		t.Fatal("duplicate device IDs were accepted")
	}
}
