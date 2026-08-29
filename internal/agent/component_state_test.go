package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

func componentState(t *testing.T, components []Component, id string) Component {
	t.Helper()
	for _, component := range components {
		if component.ID == id {
			return component
		}
	}
	t.Fatalf("component %q was not returned", id)
	return Component{}
}

func isolateComponentOwnership(t *testing.T) {
	t.Helper()
	original := componentOwnershipPath
	componentOwnershipPath = filepath.Join(t.TempDir(), "ownership.json")
	t.Cleanup(func() { componentOwnershipPath = original })
}

func TestComponentStatesMarkUnownedResourcesExternal(t *testing.T) {
	isolateComponentOwnership(t)
	discovery := Discovery{
		DockerAvailable: true,
		Containers: []string{
			"outside-xray",
			"outside-amnezia-awg",
			"vpn-panel-bypass-wb-g7",
		},
		images: map[string]bool{bypassImage("telemost"): true},
	}
	components := componentStates(discovery, true)
	for _, id := range []string{"tweaks", "docker", "xray", "xray-xhttp", "amneziawg", "bypass-wb", "bypass-telemost"} {
		component := componentState(t, components, id)
		if !component.External || component.Installed || component.CanInstall || component.CanUninstall {
			t.Errorf("%s external state = %#v", id, component)
		}
		if !strings.Contains(strings.ToLower(component.Note), "external") && !strings.Contains(component.Note, "outside SBP") {
			t.Errorf("%s external note = %q", id, component.Note)
		}
		wantRemoval := id == "tweaks" || id == "docker"
		if component.CanRemoveExternal != wantRemoval {
			t.Errorf("%s can_remove_external = %v, want %v", id, component.CanRemoveExternal, wantRemoval)
		}
	}
}

func TestComponentStatesKeepOwnedPrerequisitesManaged(t *testing.T) {
	isolateComponentOwnership(t)
	for _, id := range []string{"tweaks", "docker"} {
		if err := markComponentOwned(id, nil); err != nil {
			t.Fatal(err)
		}
	}
	components := componentStates(Discovery{DockerAvailable: true, images: map[string]bool{}}, true)
	for _, id := range []string{"tweaks", "docker"} {
		component := componentState(t, components, id)
		if component.External || !component.Installed || !component.CanUninstall {
			t.Errorf("%s managed state = %#v", id, component)
		}
	}
}

func TestProtocolComponentsExposeMachineReadableProfileVersions(t *testing.T) {
	components := componentStates(Discovery{images: map[string]bool{}}, false)
	want := map[string]struct {
		version    string
		generation int
	}{
		"xray": {version: "26.3.27", generation: 1}, "xray-xhttp": {version: "26.3.27", generation: 1},
		"amneziawg": {version: "3.1", generation: amneziaWGProfileGeneration},
		"bypass-wb": {version: "0.3.8", generation: 1}, "bypass-telemost": {version: "0.3.8", generation: 1},
		"bypass-dion": {version: "0.3.8", generation: 1}, "bypass-vk": {version: "0.3.8", generation: 1},
	}
	for id, expected := range want {
		component := componentState(t, components, id)
		if component.ProfileVersion != expected.version || component.ProfileGeneration != expected.generation || component.CanUpdate {
			t.Errorf("%s profile metadata = %#v, want revision %s/%d without an agent-side update decision", id, component, expected.version, expected.generation)
		}
	}
}
