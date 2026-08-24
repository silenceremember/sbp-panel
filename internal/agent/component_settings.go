package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const maxComponentSettingsBytes = 32 << 10

var componentSettingsDir = "/opt/vpn-panel-managed/.settings"
var componentSettingsMu sync.Mutex

func componentSettingsPath(id string) (string, error) {
	if !validComponent(id) || strings.ContainsAny(id, `/\\`) {
		return "", errors.New("unsupported component settings")
	}
	return filepath.Join(componentSettingsDir, id+".conf"), nil
}

func readComponentSettings(id string) ([]byte, bool, error) {
	path, err := componentSettingsPath(id)
	if err != nil {
		return nil, false, err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxComponentSettingsBytes {
		return nil, false, fmt.Errorf("%s settings exceed %d bytes", id, maxComponentSettingsBytes)
	}
	return body, true, nil
}

func writeComponentSettings(id string, body []byte) error {
	if len(body) == 0 || len(body) > maxComponentSettingsBytes {
		return fmt.Errorf("%s settings must contain between 1 and %d bytes", id, maxComponentSettingsBytes)
	}
	path, err := componentSettingsPath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	_ = os.Remove(temporary)
	if err := os.WriteFile(temporary, body, 0600); err != nil {
		return err
	}
	if file, err := os.OpenFile(temporary, os.O_RDWR, 0); err != nil {
		_ = os.Remove(temporary)
		return err
	} else {
		syncErr := file.Sync()
		closeErr := file.Close()
		if syncErr != nil || closeErr != nil {
			_ = os.Remove(temporary)
			return errors.Join(syncErr, closeErr)
		}
	}
	if err := os.Rename(temporary, path); err != nil {
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
	return syncParentDirectory(path)
}

func restoreComponentSettings(id string, previous []byte, existed bool) error {
	if existed {
		return writeComponentSettings(id, previous)
	}
	path, err := componentSettingsPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
