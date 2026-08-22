package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func parseXrayStats(output string, emails map[string]string) map[string][2]uint64 {
	return parseXrayStatsForProtocol(output, emails, stableXrayVariant.TrafficProtocol)
}

func newXrayConfig(private, shortID string) map[string]any {
	return newXrayConfigFor(stableXrayVariant, private, shortID, "", nil)
}

func TestConfigureXrayTraffic(t *testing.T) {
	root := map[string]any{
		"inbounds": []any{map[string]any{
			"tag": "vless-reality",
			"settings": map[string]any{"clients": []any{
				map[string]any{"id": "11111111-2222-4333-8444-555555555555", "email": "Phone"},
			}},
		}},
	}
	if !configureXrayTraffic(root) {
		t.Fatal("first configuration should report a change")
	}
	if endpoint := xrayAPIEndpoint(root); endpoint != defaultXrayStatsEndpoint {
		t.Fatalf("endpoint = %q, want %q", endpoint, defaultXrayStatsEndpoint)
	}
	api := root["api"].(map[string]any)
	services := api["services"].([]any)
	if len(services) != 2 || services[0] != "StatsService" || services[1] != "HandlerService" {
		t.Fatalf("services = %#v", services)
	}
	level := root["policy"].(map[string]any)["levels"].(map[string]any)["0"].(map[string]any)
	if level["statsUserUplink"] != true || level["statsUserDownlink"] != true {
		t.Fatalf("stats policy = %#v", level)
	}
	client := root["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)
	if client["email"] != xrayStatsEmail(client["id"].(string)) || client["level"] != 0 {
		t.Fatalf("client = %#v", client)
	}
	if configureXrayTraffic(root) {
		t.Fatal("configuration should be idempotent")
	}
}

func TestParseXrayStats(t *testing.T) {
	output := `{"stat":[
		{"name":"user>>>phone@local>>>traffic>>>downlink","value":"120"},
		{"name":"user>>>phone@local>>>traffic>>>uplink","value":30},
		{"name":"inbound>>>main>>>traffic>>>downlink","value":"999"}
	]}`
	got := parseXrayStats(output, map[string]string{"phone@local": "device-uuid"})
	if counters := got["xray:device-uuid"]; counters != [2]uint64{120, 30} {
		t.Fatalf("counters = %#v", counters)
	}
}

func TestParseAmneziaWGDump(t *testing.T) {
	output := "server-private\tserver-public\t48692\toff\n" +
		"peer-public\tpsk\t198.51.100.10:1234\t10.8.1.2/32\t1780000000\t400\t900\t25\n"
	got := parseAmneziaWGDump(output)
	if counters := got["amneziawg:peer-public"]; counters != [2]uint64{900, 400} {
		t.Fatalf("counters = %#v", counters)
	}
}

func TestUpdateMeterHandlesCounterReset(t *testing.T) {
	meter := roomTrafficMeterState{RXBytes: 100, TXBytes: 200, LastRX: 500, LastTX: 800}
	meter = updateMeter(meter, 700, 900)
	if meter.RXBytes != 300 || meter.TXBytes != 300 {
		t.Fatalf("incremented meter = %#v", meter)
	}
	meter = updateMeter(meter, 20, 30)
	if meter.RXBytes != 320 || meter.TXBytes != 330 {
		t.Fatalf("reset meter = %#v", meter)
	}
}

func TestGeneratedXrayConfigWithPinnedImage(t *testing.T) {
	if os.Getenv("CI") != "true" {
		t.Skip("the pinned-image validation runs in release CI")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is unavailable")
	}
	privateBytes := make([]byte, 32)
	for index := range privateBytes {
		privateBytes[index] = byte(index + 1)
	}
	config := newXrayConfig(base64.RawURLEncoding.EncodeToString(privateBytes), "0123456789abcdef")
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeXrayConfig(path, body); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("docker", "run", "--rm", "-v", path+":/etc/xray/config.json:ro", xrayImage, "run", "-test", "-config", "/etc/xray/config.json")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pinned Xray rejected generated config: %v\n%s", err, output)
	}
}
