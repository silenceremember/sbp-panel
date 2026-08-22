//go:build linux

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func preserveFileOwner(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect owner of %q", info.Name())
	}
	return os.Chown(path, int(stat.Uid), int(stat.Gid))
}

func syncParentDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}
