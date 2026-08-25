package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

var lifecycleState struct {
	sync.Mutex
	active string
}

// acquireLifecycle serializes every operation that can replace application
// files or change managed components. The transaction file extends the gate
// across service restarts while the watchdog is still able to roll back.
func acquireLifecycle(owner string) error {
	lifecycleState.Lock()
	defer lifecycleState.Unlock()
	if lifecycleState.active != "" {
		return fmt.Errorf("%w: %s", errLifecycleBusy, lifecycleState.active)
	}
	if amneziaWGComponentUpdatePending() && !strings.HasPrefix(owner, "amneziawg-update-finalize:") {
		return fmt.Errorf("%w: amneziawg-update", errLifecycleBusy)
	}
	if _, err := os.Lstat(updateTransactionPath); err == nil {
		return fmt.Errorf("%w: update recovery is in progress", errLifecycleBusy)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect update recovery state: %w", err)
	}
	lifecycleState.active = owner
	return nil
}

func releaseLifecycle(owner string) {
	lifecycleState.Lock()
	if lifecycleState.active == owner {
		lifecycleState.active = ""
	}
	lifecycleState.Unlock()
}
