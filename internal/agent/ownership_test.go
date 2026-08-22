package agent

import (
	"path/filepath"
	"testing"
)

func TestOwnershipManifestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownership.json")
	want := ownershipManifest{Components: map[string]ownedComponent{
		"docker": {InstalledAt: "2026-08-14T00:00:00Z", Previous: map[string]string{"package": "absent"}},
	}}
	if err := saveOwnership(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	component, ok := got.Components["docker"]
	if !ok || component.InstalledAt != want.Components["docker"].InstalledAt || component.Previous["package"] != "absent" {
		t.Fatalf("unexpected manifest: %#v", got)
	}
}

func TestMissingOwnershipManifestIsEmpty(t *testing.T) {
	manifest, err := loadOwnership(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Components) != 0 {
		t.Fatalf("unexpected components: %#v", manifest.Components)
	}
}

func TestMarkAndClearComponentOwnership(t *testing.T) {
	original := componentOwnershipPath
	componentOwnershipPath = filepath.Join(t.TempDir(), "ownership.json")
	t.Cleanup(func() { componentOwnershipPath = original })

	if err := markComponentOwned("tweaks", map[string]string{"default_qdisc": "fq_codel"}); err != nil {
		t.Fatal(err)
	}
	owned, ok := componentOwnership("tweaks")
	if !ok || owned.Previous["default_qdisc"] != "fq_codel" || owned.InstalledAt == "" {
		t.Fatalf("unexpected ownership record: %#v %v", owned, ok)
	}
	if err := clearComponentOwnership("tweaks"); err != nil {
		t.Fatal(err)
	}
	if _, ok := componentOwnership("tweaks"); ok {
		t.Fatal("ownership remained after clear")
	}
}
