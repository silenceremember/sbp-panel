package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var componentOwnershipPath = "/opt/vpn-panel-managed/.sbp-ownership.json"

type ownedComponent struct {
	InstalledAt string            `json:"installed_at"`
	Previous    map[string]string `json:"previous,omitempty"`
}

type ownershipManifest struct {
	Components map[string]ownedComponent `json:"components"`
}

var ownershipMu sync.Mutex

func loadOwnership(path string) (ownershipManifest, error) {
	manifest := ownershipManifest{Components: map[string]ownedComponent{}}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return manifest, nil
	}
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return ownershipManifest{}, err
	}
	if manifest.Components == nil {
		manifest.Components = map[string]ownedComponent{}
	}
	return manifest, nil
}

func saveOwnership(path string, manifest ownershipManifest) error {
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, body, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func markComponentOwned(id string, previous map[string]string) error {
	ownershipMu.Lock()
	defer ownershipMu.Unlock()
	manifest, err := loadOwnership(componentOwnershipPath)
	if err != nil {
		return err
	}
	manifest.Components[id] = ownedComponent{InstalledAt: time.Now().UTC().Format(time.RFC3339), Previous: previous}
	return saveOwnership(componentOwnershipPath, manifest)
}

func componentOwnership(id string) (ownedComponent, bool) {
	value, ok, _ := checkedComponentOwnership(id)
	return value, ok
}

func checkedComponentOwnership(id string) (ownedComponent, bool, error) {
	ownershipMu.Lock()
	defer ownershipMu.Unlock()
	manifest, err := loadOwnership(componentOwnershipPath)
	if err != nil {
		return ownedComponent{}, false, err
	}
	value, ok := manifest.Components[id]
	return value, ok, nil
}

func clearComponentOwnership(id string) error {
	ownershipMu.Lock()
	defer ownershipMu.Unlock()
	manifest, err := loadOwnership(componentOwnershipPath)
	if err != nil {
		return err
	}
	delete(manifest.Components, id)
	if len(manifest.Components) == 0 {
		if err := os.Remove(componentOwnershipPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_ = os.Remove(componentOwnershipPath + ".tmp")
		return nil
	}
	return saveOwnership(componentOwnershipPath, manifest)
}
