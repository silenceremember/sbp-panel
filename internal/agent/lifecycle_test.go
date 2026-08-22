package agent

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/silenceremember/sbp-panel/internal/config"
)

func TestCleanupInterruptedInstallRemovesMarkedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "component")
	if err := writeInstallMarker(dir, installMarker{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "partial-config"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}

	cleanupInterruptedInstall(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("interrupted install directory remained: %v", err)
	}
}

func TestCleanupInterruptedInstallPreservesCompletedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "component")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	cleanupInterruptedInstall(dir)
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("completed install was changed: %v", err)
	}
}

func TestCleanupInterruptedMarkerWriteRemovesEmptyTransaction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "component")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, installMarkerName+".tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}

	cleanupInterruptedInstall(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("partial marker directory remained: %v", err)
	}
}

func useTemporaryLifecycleState(t *testing.T) {
	t.Helper()
	originalTransaction := updateTransactionPath
	originalProgress := updateProgressPath
	updateTransactionPath = filepath.Join(t.TempDir(), "update-transaction.json")
	updateProgressPath = filepath.Join(t.TempDir(), "update-progress.json")
	lifecycleState.Lock()
	lifecycleState.active = ""
	lifecycleState.Unlock()
	updateJobMu.Lock()
	updateJobRunning = false
	updateJobMu.Unlock()
	t.Cleanup(func() {
		updateTransactionPath = originalTransaction
		updateProgressPath = originalProgress
		lifecycleState.Lock()
		lifecycleState.active = ""
		lifecycleState.Unlock()
		updateJobMu.Lock()
		updateJobRunning = false
		updateJobMu.Unlock()
	})
}

func TestLifecycleGateSerializesUpdateAndComponentJobs(t *testing.T) {
	useTemporaryLifecycleState(t)
	if err := acquireLifecycle("component:test"); err != nil {
		t.Fatal(err)
	}
	if _, err := startUpdateJob(config.Config{}, &http.Client{}, false); !errors.Is(err, errLifecycleBusy) {
		t.Fatalf("update overlapped a component operation: %v", err)
	}
	releaseLifecycle("component:test")

	if err := acquireLifecycle("panel-update"); err != nil {
		t.Fatal(err)
	}
	inst := &installer{jobs: map[string]installJob{}}
	if err := inst.startJob("test", func(string, config.Config) (string, error) { return "", nil }); !errors.Is(err, errLifecycleBusy) {
		t.Fatalf("component operation overlapped an update: %v", err)
	}
	releaseLifecycle("panel-update")
}

func TestLifecycleGateHonorsDurableUpdateTransaction(t *testing.T) {
	useTemporaryLifecycleState(t)
	if err := os.MkdirAll(filepath.Dir(updateTransactionPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updateTransactionPath, []byte(`{"phase":"installed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := acquireLifecycle("component:test"); !errors.Is(err, errLifecycleBusy) {
		t.Fatalf("durable update transaction did not block a lifecycle operation: %v", err)
	}
}
