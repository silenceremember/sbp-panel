package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/silenceremember/sbp-panel/internal/store"
)

func (s *server) saveProtocolSettings(w http.ResponseWriter, method string, body []byte) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		fail(w, 400, err)
		return
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	path := "/v1/components/" + method + "/settings"
	var previous struct {
		Settings struct {
			Content string `json:"content"`
		} `json:"settings"`
	}
	if err := s.callAgentJSON(http.MethodGet, path, nil, &previous); err != nil {
		fail(w, 502, err)
		return
	}
	devices, err := s.db.ListAllDevices()
	if err != nil {
		fail(w, 500, err)
		return
	}
	var result map[string]any
	if err := s.callAgentJSON(http.MethodPut, path, input, &result); err != nil {
		fail(w, 400, err)
		return
	}
	rollback := func(err error) {
		restoreErr := s.callAgentJSON(http.MethodPut, path, map[string]string{"content": previous.Settings.Content}, nil)
		fail(w, 502, errors.Join(err, restoreErr))
	}
	updates := []store.DeviceProfileUpdate{}
	for _, device := range devices {
		if device.Method != method {
			continue
		}
		profile, err := s.renderCredential(device.Name, method, device.Credential)
		if err != nil {
			rollback(fmt.Errorf("refresh %s: %w", device.Name, err))
			return
		}
		updates = append(updates, store.DeviceProfileUpdate{DeviceID: device.ID, Name: device.Name, Credential: profile.Credential, ProfileGeneration: profile.ProfileGeneration, ProtocolVersion: profile.ProtocolVersion})
	}
	if err := s.db.UpdateDeviceProfiles(updates); err != nil {
		rollback(err)
		return
	}
	result["updated_profiles"] = len(updates)
	jsonOut(w, 200, result)
}
