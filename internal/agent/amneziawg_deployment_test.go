package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestAmneziaWGDeploymentRevision(t *testing.T) {
	for _, test := range []struct {
		name, engine, revision string
		current                bool
	}{
		{"unrecorded", "", "", false},
		{"engine changed", "3.1.20260814", amneziaWGDeploymentRevision, false},
		{"configuration changed", awgVersion, "outdated", false},
		{"current", awgVersion, amneziaWGDeploymentRevision, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"engine": test.engine, "deployment_revision": test.revision})
			if amneziaWGDeploymentCurrent(body) != test.current {
				t.Fatal("wrong Update eligibility")
			}
		})
	}
	for _, body := range [][]byte{nil, []byte("broken"), []byte(`{"engine":123}`)} {
		if amneziaWGDeploymentCurrent(body) {
			t.Fatal("unverified deployment marked current")
		}
	}
}

func TestAmneziaWGFinalizeKeepsRollbackUntilDurableCommit(t *testing.T) {
	for _, failure := range []string{"verify", "tag-old", "tag-new", "save", "cleanup", ""} {
		t.Run(failure, func(t *testing.T) {
			snapshot := amneziaWGComponentUpdateSnapshot{Phase: "ready", PreviousImageID: "old", CandidateImageID: "new"}
			var calls []string
			step := func(name string) error {
				calls = append(calls, name)
				if name == failure {
					return errors.New("injected failure")
				}
				return nil
			}
			ops := amneziaWGFinalizeOps{
				verify: func() error { return step("verify") },
				tag:    func(image, tag string) error { return step("tag-" + image) },
				save: func(next amneziaWGComponentUpdateSnapshot) error {
					if err := step("save"); err != nil {
						return err
					}
					snapshot = next
					return nil
				},
				cleanup: func() error { return step("cleanup") },
			}
			err := finalizeAmneziaWGDeployment(snapshot, ops)
			if (err != nil) != (failure != "") {
				t.Fatalf("error=%v", err)
			}
			if failure == "cleanup" || failure == "" {
				if snapshot.Phase != "committed" {
					t.Fatal("cleanup ran without durable commit")
				}
			} else if snapshot.Phase != "ready" {
				t.Fatal("failed prepare lost rollback")
			}
			if failure == "cleanup" {
				calls = nil
				ops.cleanup = func() error { return step("done") }
				if err := finalizeAmneziaWGDeployment(snapshot, ops); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(calls, []string{"verify", "done"}) {
					t.Fatalf("retry repeated destructive commit: %v", calls)
				}
			}
		})
	}
	if err := finalizeAmneziaWGDeployment(amneziaWGComponentUpdateSnapshot{Phase: "prepared"}, amneziaWGFinalizeOps{}); err == nil {
		t.Fatal("unverified candidate committed")
	}
}
