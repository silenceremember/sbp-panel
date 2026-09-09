package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type portableDevice struct {
	Name        string `json:"name"`
	Method      string `json:"method"`
	Format      string `json:"format,omitempty"`
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
type portableGroup struct {
	Name      string           `json:"name"`
	Contact   string           `json:"contact"`
	ExpiresAt string           `json:"expires_at"`
	Unlimited bool             `json:"unlimited"`
	Devices   []portableDevice `json:"devices"`
}
type portableConfiguration struct {
	Format     string                     `json:"format"`
	Groups     []portableGroup            `json:"groups"`
	Components []string                   `json:"components"`
	Settings   map[string]string          `json:"settings"`
	Cookies    map[string]json.RawMessage `json:"cookies"`
}

func (s *server) configuration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var result portableConfiguration
	if r.Method == http.MethodGet {
		s.credentialMu.Lock()
		defer s.credentialMu.Unlock()
		if err := s.callAgentJSON(http.MethodGet, "/v1/configuration", nil, &result); err != nil {
			fail(w, 502, err)
			return
		}
		result.Format = "sbp-configuration"
		groups, err := s.db.ListGroups()
		if err != nil {
			fail(w, 500, err)
			return
		}
		result.Groups = []portableGroup{}
		for _, g := range groups {
			group := portableGroup{Name: g.Name, Contact: g.Contact, Unlimited: g.Unlimited, Devices: []portableDevice{}}
			if g.ExpiresAt != nil {
				group.ExpiresAt = *g.ExpiresAt
			}
			devices, err := s.db.ListDevices(g.ID)
			if err != nil {
				fail(w, 500, err)
				return
			}
			for _, d := range devices {
				device := portableDevice{Name: d.Name, Method: d.Method, Format: d.Format, Enabled: d.Enabled}
				if d.Method == "xray" || d.Method == "xray-xhttp" {
					if link, err := url.Parse(d.Credential); err == nil {
						device.Fingerprint = link.Query().Get("fp")
					}
				}
				group.Devices = append(group.Devices, device)
			}
			result.Groups = append(result.Groups, group)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="sbp-configuration.json"`)
	} else {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20+1))
		if err != nil || len(body) > 4<<20 || json.Unmarshal(body, &result) != nil {
			fail(w, 400, errors.New("invalid configuration JSON (maximum 4 MiB)"))
			return
		}
		if err := validatePortableConfiguration(result); err != nil {
			fail(w, 400, err)
			return
		}
		if err := s.callAgentJSON(http.MethodPost, "/v1/configuration/validate", result.Settings, nil); err != nil {
			fail(w, 400, err)
			return
		}

	}
	jsonOut(w, 200, result)
}

func validatePortableConfiguration(c portableConfiguration) error {
	if c.Format != "sbp-configuration" {
		return errors.New("expected an SBP configuration export")
	}
	components := map[string]bool{}
	for _, id := range c.Components {
		switch id {
		case "docker", "tweaks", "xray", "xray-xhttp", "amneziawg", "bypass-wb", "bypass-telemost", "bypass-dion", "bypass-vk":
		default:
			return fmt.Errorf("unknown component %q", id)
		}
		components[id] = true
	}
	for id, content := range c.Settings {
		if id != "tweaks" && id != "xray" && id != "xray-xhttp" && id != "amneziawg" {
			return fmt.Errorf("unknown component settings %q", id)
		}
		if len(content) > 32<<10 {
			return errors.New("component settings exceed 32 KiB")
		}
	}
	for provider, cookies := range c.Cookies {
		if provider != "wbstream" && provider != "telemost" && provider != "dion" && provider != "vk" {
			return errors.New("unknown cookie provider")
		}
		if len(cookies) > 256<<10 {
			return errors.New("provider cookies exceed 256 KiB")
		}
	}
	names := map[string]bool{}
	for _, group := range c.Groups {
		name := strings.ToLower(strings.TrimSpace(group.Name))
		if name == "" || names[name] {
			return errors.New("group names must be nonempty and unique")
		}
		names[name] = true
		if !group.Unlimited {
			if _, err := time.Parse(time.RFC3339, group.ExpiresAt); err != nil {
				return fmt.Errorf("invalid expiration for %s", group.Name)
			}
		}
		devices := map[string]bool{}
		for _, d := range group.Devices {
			key := strings.ToLower(strings.TrimSpace(d.Name)) + "/" + d.Method + "/" + d.Format
			if strings.TrimSpace(d.Name) == "" || devices[key] {
				return fmt.Errorf("duplicate or empty device in %s", group.Name)
			}
			devices[key] = true
			if _, ok := managedMethodByComponent[d.Method]; !ok || !components[d.Method] {
				return fmt.Errorf("missing component for %s", d.Name)
			}
			if d.Format != "" && d.Format != "app" && d.Format != "native" {
				return errors.New("invalid profile format")
			}
			if d.Fingerprint != "" {
				if _, err := withXrayFingerprint("vless://id@example.com:443", d.Fingerprint); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
