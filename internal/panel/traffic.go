package panel

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/silenceremember/sbp-panel/internal/store"
)

func (s *server) processTrafficMetrics(body []byte) []byte {
	var sample struct {
		MonthKey      string `json:"month_key"`
		DeviceTraffic []struct {
			Protocol string `json:"protocol"`
			PublicID string `json:"public_id"`
			RXBytes  uint64 `json:"rx_bytes"`
			TXBytes  uint64 `json:"tx_bytes"`
		} `json:"device_traffic"`
		BypassRooms []struct {
			GroupID  int64  `json:"group_id"`
			Provider string `json:"provider"`
			RXBytes  int64  `json:"rx_bytes"`
			TXBytes  int64  `json:"tx_bytes"`
		} `json:"bypass_rooms"`
	}
	if json.Unmarshal(body, &sample) != nil {
		return body
	}
	devices, err := s.db.ListAllDevices()
	if err != nil {
		return withoutPublicTrafficIDs(body)
	}
	byPublicID := map[string][]store.Device{}
	for _, device := range devices {
		if publicID, ok := deviceTrafficPublicID(device); ok {
			key := device.Method + "\x00" + publicID
			byPublicID[key] = append(byPublicID[key], device)
		}
	}
	deviceSamples := make([]store.DeviceTrafficSample, 0, len(sample.DeviceTraffic))
	managedDevices := make([]map[string]any, 0, len(sample.DeviceTraffic))
	for _, traffic := range sample.DeviceTraffic {
		matches := byPublicID[traffic.Protocol+"\x00"+traffic.PublicID]
		if len(matches) != 1 || traffic.RXBytes > math.MaxInt64 || traffic.TXBytes > math.MaxInt64 {
			continue
		}
		deviceSamples = append(deviceSamples, store.DeviceTrafficSample{
			DeviceID: matches[0].ID, GroupID: matches[0].GroupID, Protocol: traffic.Protocol,
			RXBytes: int64(traffic.RXBytes), TXBytes: int64(traffic.TXBytes),
		})
		managedDevices = append(managedDevices, map[string]any{
			"device_id": matches[0].ID, "group_id": matches[0].GroupID,
			"rx_bytes": traffic.RXBytes, "tx_bytes": traffic.TXBytes,
		})
	}
	s.persistTrafficSample(sample.MonthKey, deviceSamples, sample.BypassRooms)
	return managedTrafficResponse(body, managedDevices)
}

func (s *server) persistTrafficSample(month string, devices []store.DeviceTrafficSample, rooms []struct {
	GroupID  int64  `json:"group_id"`
	Provider string `json:"provider"`
	RXBytes  int64  `json:"rx_bytes"`
	TXBytes  int64  `json:"tx_bytes"`
}) {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if !s.metricsSaved.IsZero() && time.Since(s.metricsSaved) < time.Minute {
		return
	}
	if err := s.db.SetDeviceTrafficSamples(month, devices); err != nil {
		return
	}
	for _, room := range rooms {
		if err := s.db.SetGroupProtocolTraffic(room.GroupID, room.Provider, month, room.RXBytes, room.TXBytes); err != nil {
			return
		}
	}
	s.metricsSaved = time.Now()
}

func managedTrafficResponse(body []byte, managed []map[string]any) []byte {
	var response map[string]any
	if json.Unmarshal(body, &response) != nil {
		return body
	}
	delete(response, "device_traffic")
	if managed != nil {
		response["managed_devices"] = managed
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return body
	}
	return encoded
}

func withoutPublicTrafficIDs(body []byte) []byte {
	return managedTrafficResponse(body, nil)
}

func deviceTrafficPublicID(device store.Device) (string, bool) {
	switch device.Method {
	case "xray", "xray-xhttp":
		value := strings.TrimSpace(device.Credential)
		if !strings.HasPrefix(value, "vless://") {
			return "", false
		}
		id, _, ok := strings.Cut(strings.TrimPrefix(value, "vless://"), "@")
		return strings.TrimSpace(id), ok && strings.TrimSpace(id) != ""
	case "amneziawg":
		private := wireGuardConfigValue(device.Credential, "[Interface]", "PrivateKey")
		raw, err := base64.StdEncoding.DecodeString(private)
		if err != nil || len(raw) != 32 {
			return "", false
		}
		key, err := ecdh.X25519().NewPrivateKey(raw)
		if err != nil {
			return "", false
		}
		return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), true
	default:
		return "", false
	}
}

func wireGuardConfigValue(body, section, key string) string {
	current := ""
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = line
			continue
		}
		if current != section {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) == key {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
