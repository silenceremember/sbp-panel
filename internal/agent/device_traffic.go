package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const defaultXrayStatsEndpoint = "127.0.0.1:10085"

type DeviceTrafficMetrics struct {
	Protocol string `json:"protocol"`
	PublicID string `json:"public_id"`
	RXBytes  uint64 `json:"rx_bytes"`
	TXBytes  uint64 `json:"tx_bytes"`
}

func updateMeter(meter roomTrafficMeterState, rx, tx uint64) roomTrafficMeterState {
	if rx >= meter.LastRX {
		meter.RXBytes += rx - meter.LastRX
	} else {
		meter.RXBytes += rx
	}
	if tx >= meter.LastTX {
		meter.TXBytes += tx - meter.LastTX
	} else {
		meter.TXBytes += tx
	}
	meter.LastRX, meter.LastTX = rx, tx
	return meter
}

func xrayStatsEmail(id string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return "sbp-" + hex.EncodeToString(sum[:8]) + "@local"
}

func xrayAPIEndpoint(root map[string]any) string {
	api, _ := root["api"].(map[string]any)
	if listen := strings.TrimSpace(fmt.Sprint(api["listen"])); listen != "" && listen != "<nil>" {
		return listen
	}
	tag := strings.TrimSpace(fmt.Sprint(api["tag"]))
	if tag == "" || tag == "<nil>" {
		tag = "api"
	}
	inbounds, _ := root["inbounds"].([]any)
	for _, raw := range inbounds {
		inbound, _ := raw.(map[string]any)
		if strings.TrimSpace(fmt.Sprint(inbound["tag"])) != tag {
			continue
		}
		port, ok := integerValue(inbound["port"])
		if !ok || port < 1 || port > 65535 {
			continue
		}
		host := strings.TrimSpace(fmt.Sprint(inbound["listen"]))
		if host == "" || host == "<nil>" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		return fmt.Sprintf("%s:%d", host, port)
	}
	return ""
}

func integerValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case json.Number:
		v, err := typed.Int64()
		return v, err == nil
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case string:
		v, err := strconv.ParseInt(typed, 10, 64)
		return v, err == nil
	default:
		return 0, false
	}
}

func configureXrayTraffic(root map[string]any) bool {
	changed := false
	if _, ok := root["stats"].(map[string]any); !ok {
		root["stats"] = map[string]any{}
		changed = true
	}
	policy, _ := root["policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
		root["policy"] = policy
		changed = true
	}
	levels, _ := policy["levels"].(map[string]any)
	if levels == nil {
		levels = map[string]any{}
		policy["levels"] = levels
		changed = true
	}
	level, _ := levels["0"].(map[string]any)
	if level == nil {
		level = map[string]any{}
		levels["0"] = level
		changed = true
	}
	for _, key := range []string{"statsUserUplink", "statsUserDownlink"} {
		if enabled, _ := level[key].(bool); !enabled {
			level[key] = true
			changed = true
		}
	}

	endpoint := xrayAPIEndpoint(root)
	api, _ := root["api"].(map[string]any)
	if api == nil {
		api = map[string]any{}
		root["api"] = api
		changed = true
	}
	if tag := strings.TrimSpace(fmt.Sprint(api["tag"])); tag == "" || tag == "<nil>" {
		api["tag"] = "api"
		changed = true
	}
	services, _ := api["services"].([]any)
	hasStats := false
	hasHandler := false
	for _, service := range services {
		switch fmt.Sprint(service) {
		case "StatsService":
			hasStats = true
		case "HandlerService":
			hasHandler = true
		}
	}
	if !hasStats {
		services = append(services, "StatsService")
		api["services"] = services
		changed = true
	}
	if !hasHandler {
		services = append(services, "HandlerService")
		api["services"] = services
		changed = true
	}
	if endpoint == "" {
		api["listen"] = defaultXrayStatsEndpoint
		changed = true
	}

	inbounds, _ := root["inbounds"].([]any)
	for _, rawInbound := range inbounds {
		inbound, _ := rawInbound.(map[string]any)
		settings, _ := inbound["settings"].(map[string]any)
		clients, _ := settings["clients"].([]any)
		for _, rawClient := range clients {
			client, _ := rawClient.(map[string]any)
			id := strings.TrimSpace(fmt.Sprint(client["id"]))
			if id == "" || id == "<nil>" {
				continue
			}
			email := xrayStatsEmail(id)
			if fmt.Sprint(client["email"]) != email {
				client["email"] = email
				changed = true
			}
			if levelValue, ok := integerValue(client["level"]); !ok || levelValue != 0 {
				client["level"] = 0
				changed = true
			}
		}
	}
	return changed
}

func linePresent(lines, want string) bool {
	for _, line := range strings.Split(lines, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func readDeviceCounters() map[string][2]uint64 {
	result := readXrayDeviceCounters()
	for key, counters := range readAmneziaWGDeviceCounters() {
		result[key] = counters
	}
	return result
}

func readXrayDeviceCounters() map[string][2]uint64 {
	result := map[string][2]uint64{}
	for _, variant := range allXrayVariants() {
		for key, counters := range readXrayVariantDeviceCounters(variant) {
			result[key] = counters
		}
	}
	return result
}

func readXrayVariantDeviceCounters(variant xrayVariant) map[string][2]uint64 {
	result := map[string][2]uint64{}
	path := variant.configPath()
	if path == "" {
		return result
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return result
	}
	emails := map[string]string{}
	for _, rawInbound := range anySlice(root["inbounds"]) {
		inbound, _ := rawInbound.(map[string]any)
		settings, _ := inbound["settings"].(map[string]any)
		for _, rawClient := range anySlice(settings["clients"]) {
			client, _ := rawClient.(map[string]any)
			email := strings.TrimSpace(fmt.Sprint(client["email"]))
			id := strings.TrimSpace(fmt.Sprint(client["id"]))
			if email != "" && email != "<nil>" && id != "" && id != "<nil>" {
				emails[email] = id
			}
		}
	}
	endpoint := xrayAPIEndpoint(root)
	if endpoint == "" {
		return result
	}
	command := fixedCommand("docker", "exec", variant.Container, "xray", "api", "statsquery", "--server="+endpoint, "-pattern", "user>>>")
	if !command.OK {
		return result
	}
	return parseXrayStatsForProtocol(command.Output, emails, variant.TrafficProtocol)
}

func parseXrayStatsForProtocol(output string, emails map[string]string, protocol string) map[string][2]uint64 {
	result := map[string][2]uint64{}
	var response struct {
		Stats []struct {
			Name  string `json:"name"`
			Value any    `json:"value"`
		} `json:"stat"`
	}
	if json.Unmarshal([]byte(output), &response) != nil {
		return result
	}
	for _, stat := range response.Stats {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}
		id := emails[parts[1]]
		if id == "" {
			continue
		}
		value, ok := uintValue(stat.Value)
		if !ok {
			continue
		}
		key := protocol + ":" + id
		counters := result[key]
		switch parts[3] {
		case "downlink":
			counters[0] = value
		case "uplink":
			counters[1] = value
		default:
			continue
		}
		result[key] = counters
	}
	return result
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func uintValue(value any) (uint64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed < 0 {
			return 0, false
		}
		return uint64(typed), true
	case string:
		parsed, err := strconv.ParseUint(typed, 10, 64)
		return parsed, err == nil
	case json.Number:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func readAmneziaWGDeviceCounters() map[string][2]uint64 {
	command := fixedCommand("docker", "exec", "amnezia-awg2", "awg", "show", "awg0", "dump")
	if !command.OK {
		return map[string][2]uint64{}
	}
	return parseAmneziaWGDump(command.Output)
}

func parseAmneziaWGDump(output string) map[string][2]uint64 {
	result := map[string][2]uint64{}
	for index, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if index == 0 {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 8 {
			continue
		}
		received, rxErr := strconv.ParseUint(fields[5], 10, 64)
		sent, txErr := strconv.ParseUint(fields[6], 10, 64)
		if rxErr != nil || txErr != nil || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		// The interface reports bytes from the server's perspective. Present
		// download as RX and upload as TX from the device's perspective.
		result["amneziawg:"+strings.TrimSpace(fields[0])] = [2]uint64{sent, received}
	}
	return result
}
