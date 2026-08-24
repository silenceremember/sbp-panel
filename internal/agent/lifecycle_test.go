package agent

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	if err := inst.startJob("test", "install", func(string, config.Config) (string, error) { return "", nil }); !errors.Is(err, errLifecycleBusy) {
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

func TestInstallerReportsAndDeduplicatesActiveLifecycle(t *testing.T) {
	useTemporaryLifecycleState(t)
	started := make(chan struct{})
	finish := make(chan struct{})
	var finishOnce sync.Once
	defer finishOnce.Do(func() { close(finish) })
	inst := &installer{jobs: map[string]installJob{}}
	operation := func(string, config.Config) (string, error) {
		close(started)
		<-finish
		return "installed", nil
	}
	if err := inst.startJob("xray", "install", operation); err != nil {
		t.Fatal(err)
	}
	<-started
	job := inst.lifecycle()
	if job.Status != "running" || job.ComponentID != "xray" || job.Operation != "install" {
		t.Fatalf("unexpected active lifecycle: %+v", job)
	}
	if err := inst.startJob("xray", "install", operation); err != nil {
		t.Fatalf("identical active operation was not idempotent: %v", err)
	}
	if err := inst.startJob("xray", "uninstall", operation); !errors.Is(err, errLifecycleBusy) {
		t.Fatalf("conflicting operation on the active component was accepted: %v", err)
	}
	finishOnce.Do(func() { close(finish) })
	deadline := time.Now().Add(time.Second)
	for inst.lifecycle().Status != "idle" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if job := inst.lifecycle(); job.Status != "idle" {
		t.Fatalf("completed lifecycle remained active: %+v", job)
	}
	job = inst.get("xray")
	if job.Status != "done" || job.Output != "installed" || job.Operation != "install" {
		t.Fatalf("completed lifecycle lost operation metadata: %+v", job)
	}
}
