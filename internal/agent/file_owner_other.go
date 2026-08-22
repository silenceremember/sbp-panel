//go:build !linux

package agent

import "os"

func preserveFileOwner(_ string, _ os.FileInfo) error { return nil }

func syncParentDirectory(_ string) error { return nil }
