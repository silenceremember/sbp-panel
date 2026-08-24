package agent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInspectDockerComposeDistinguishesManagedAndExternalState(t *testing.T) {
	isolateComponentOwnership(t)
	externalOps := dockerComposeOps{
		installedPackages: func() (map[string]struct{}, error) {
			return map[string]struct{}{dockerComposePackageName: {}}, nil
		},
		run: func(name string, args ...string) (string, error) {
			return "v2.37.1\n", nil
		},
	}
	external := inspectDockerCompose(true, externalOps)
	if !external.External || !external.Installed || external.Managed || external.CanInstall || external.CanUninstall || !external.CanRemoveExternal || external.Version != "v2.37.1" {
		t.Fatalf("external state = %#v", external)
	}

	if err := markComponentOwned(dockerComposeComponentID, map[string]string{dockerComposePackageName: "absent"}); err != nil {
		t.Fatal(err)
	}
	managed := inspectDockerCompose(true, externalOps)
	if managed.External || !managed.Installed || !managed.Managed || managed.CanInstall || !managed.CanUninstall || managed.CanRemoveExternal {
		t.Fatalf("managed state = %#v", managed)
	}
}

func TestInspectDockerComposeOffersRepairWithoutAdoptingBrokenExternalPackage(t *testing.T) {
	isolateComponentOwnership(t)
	packageInstalled := false
	ops := dockerComposeOps{
		installedPackages: func() (map[string]struct{}, error) {
			packages := map[string]struct{}{}
			if packageInstalled {
				packages[dockerComposePackageName] = struct{}{}
			}
			return packages, nil
		},
		run: func(string, ...string) (string, error) { return "", errors.New("compose unavailable") },
	}
	missing := inspectDockerCompose(true, ops)
	if !missing.CanInstall || missing.CanUninstall || missing.External || missing.Managed {
		t.Fatalf("missing state = %#v", missing)
	}

	packageInstalled = true
	external := inspectDockerCompose(true, ops)
	if !external.External || external.CanInstall || external.CanUninstall || !external.CanRemoveExternal || external.Installed {
		t.Fatalf("broken external state = %#v", external)
	}

	if err := markComponentOwned(dockerComposeComponentID, map[string]string{dockerComposePackageName: "absent"}); err != nil {
		t.Fatal(err)
	}
	packageInstalled = false
	repair := inspectDockerCompose(true, ops)
	if !repair.Managed || !repair.CanInstall || !repair.CanUninstall || repair.Installed || !strings.Contains(repair.Note, "repairs") {
		t.Fatalf("repair state = %#v", repair)
	}
}

func TestInspectDockerComposeDoesNotOfferRemovalForExternalCLIPlugin(t *testing.T) {
	isolateComponentOwnership(t)
	state := inspectDockerCompose(true, dockerComposeOps{
		installedPackages: func() (map[string]struct{}, error) { return map[string]struct{}{}, nil },
		run:               func(string, ...string) (string, error) { return "v2.37.1", nil },
	})
	if !state.External || state.Installed || state.CanInstall || state.CanUninstall || state.CanRemoveExternal {
		t.Fatalf("external CLI plugin state = %#v", state)
	}
	if !strings.Contains(state.Note, "cannot remove") {
		t.Fatalf("external CLI plugin note = %q", state.Note)
	}
}

func TestInspectDockerComposeFailsClosedWhenPackageInventoryIsUnavailable(t *testing.T) {
	isolateComponentOwnership(t)
	if err := markComponentOwned(dockerComposeComponentID, map[string]string{dockerComposePackageName: "absent"}); err != nil {
		t.Fatal(err)
	}
	state := inspectDockerCompose(true, dockerComposeOps{
		installedPackages: func() (map[string]struct{}, error) {
			return nil, errors.New("dpkg unavailable")
		},
	})
	if !state.InspectionFailed || !state.Managed || state.CanInstall || state.CanUninstall || state.Installed || state.External {
		t.Fatalf("failed inspection state = %#v", state)
	}
}

func TestInstallDockerComposeRecordsOwnershipAfterValidation(t *testing.T) {
	isolateComponentOwnership(t)
	packageInstalled := false
	composeChecks := 0
	var calls []string
	ops := dockerComposeOps{
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		installedPackages: func() (map[string]struct{}, error) {
			packages := map[string]struct{}{}
			if packageInstalled {
				packages[dockerComposePackageName] = struct{}{}
			}
			return packages, nil
		},
		run: func(name string, args ...string) (string, error) {
			call := strings.TrimSpace(name + " " + strings.Join(args, " "))
			calls = append(calls, call)
			switch call {
			case "docker compose version --short":
				composeChecks++
				if composeChecks == 1 {
					return "", errors.New("not installed")
				}
				return "v2.37.1\n", nil
			case "apt-get update":
				return "updated", nil
			case "apt-get install -y --no-install-recommends docker-compose-v2":
				packageInstalled = true
				return "installed", nil
			default:
				return "", errors.New("unexpected command: " + call)
			}
		},
	}
	output, err := installDockerComposeWithOps(ops)
	if err != nil {
		t.Fatal(err)
	}
	if output != "Docker Compose v2 v2.37.1 installed" {
		t.Fatalf("output = %q", output)
	}
	owned, ok := componentOwnership(dockerComposeComponentID)
	if !ok || owned.Previous[dockerComposePackageName] != "absent" {
		t.Fatalf("ownership = %#v, %v", owned, ok)
	}
	wantCalls := []string{
		"docker compose version --short",
		"apt-get update",
		"apt-get install -y --no-install-recommends docker-compose-v2",
		"docker compose version --short",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestInstallDockerComposeRollsBackFailedValidation(t *testing.T) {
	isolateComponentOwnership(t)
	packageInstalled := false
	var calls []string
	ops := dockerComposeOps{
		lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		installedPackages: func() (map[string]struct{}, error) {
			packages := map[string]struct{}{}
			if packageInstalled {
				packages[dockerComposePackageName] = struct{}{}
			}
			return packages, nil
		},
		run: func(name string, args ...string) (string, error) {
			call := strings.TrimSpace(name + " " + strings.Join(args, " "))
			calls = append(calls, call)
			switch call {
			case "apt-get update":
				return "", nil
			case "apt-get install -y --no-install-recommends docker-compose-v2":
				packageInstalled = true
				return "partial", nil
			case "apt-get purge -y docker-compose-v2":
				packageInstalled = false
				return "removed", nil
			case "docker compose version --short":
				return "", errors.New("compose unavailable")
			default:
				return "", errors.New("unexpected command: " + call)
			}
		},
	}
	if _, err := installDockerComposeWithOps(ops); err == nil || !strings.Contains(err.Error(), "validate Docker Compose v2") {
		t.Fatalf("validation failure = %v", err)
	}
	if packageInstalled {
		t.Fatal("failed installation was not rolled back")
	}
	if _, owned := componentOwnership(dockerComposeComponentID); owned {
		t.Fatal("failed installation recorded ownership")
	}
	if calls[len(calls)-1] != "apt-get purge -y docker-compose-v2" {
		t.Fatalf("last call = %q, want rollback purge", calls[len(calls)-1])
	}
}

func TestInstallDockerComposeRollsBackWhenOwnershipCannotBeRecorded(t *testing.T) {
	originalOwnershipPath := componentOwnershipPath
	componentOwnershipPath = filepath.Join(t.TempDir(), "ownership-directory")
	if err := os.Mkdir(componentOwnershipPath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { componentOwnershipPath = originalOwnershipPath })

	packageInstalled := false
	composeChecks := 0
	ops := dockerComposeOps{
		lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		installedPackages: func() (map[string]struct{}, error) {
			packages := map[string]struct{}{}
			if packageInstalled {
				packages[dockerComposePackageName] = struct{}{}
			}
			return packages, nil
		},
		run: func(name string, args ...string) (string, error) {
			signature := strings.TrimSpace(name + " " + strings.Join(args, " "))
			switch signature {
			case "docker compose version --short":
				composeChecks++
				if composeChecks == 1 {
					return "", errors.New("not installed")
				}
				return "v2.37.1", nil
			case "apt-get update":
				return "", nil
			case "apt-get install -y --no-install-recommends docker-compose-v2":
				packageInstalled = true
				return "installed", nil
			case "apt-get purge -y docker-compose-v2":
				packageInstalled = false
				return "removed", nil
			default:
				return "", errors.New("unexpected command: " + signature)
			}
		},
	}
	_, err := installDockerComposeWithOps(ops)
	if err == nil || !strings.Contains(err.Error(), "ownership could not be recorded") {
		t.Fatalf("ownership failure = %v", err)
	}
	if packageInstalled {
		t.Fatal("ownership failure left Docker Compose v2 installed")
	}
}

func TestInstallDockerComposeRefusesExternalPackage(t *testing.T) {
	isolateComponentOwnership(t)
	var calls []string
	ops := dockerComposeOps{
		lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		installedPackages: func() (map[string]struct{}, error) {
			return map[string]struct{}{dockerComposePackageName: {}}, nil
		},
		run: func(name string, args ...string) (string, error) {
			calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
			return "v2.37.1", nil
		},
	}
	if _, err := installDockerComposeWithOps(ops); err == nil || !strings.Contains(err.Error(), "outside SBP") {
		t.Fatalf("external installation was accepted: %v", err)
	}
	if len(calls) != 1 || calls[0] != "docker compose version --short" {
		t.Fatalf("external installation calls = %#v", calls)
	}
}

func TestUninstallDockerComposeClearsOwnershipOnlyAfterPackageRemoval(t *testing.T) {
	isolateComponentOwnership(t)
	if err := markComponentOwned(dockerComposeComponentID, map[string]string{dockerComposePackageName: "absent"}); err != nil {
		t.Fatal(err)
	}
	packageInstalled := true
	ops := dockerComposeOps{
		installedPackages: func() (map[string]struct{}, error) {
			packages := map[string]struct{}{}
			if packageInstalled {
				packages[dockerComposePackageName] = struct{}{}
			}
			return packages, nil
		},
		run: func(name string, args ...string) (string, error) {
			if strings.TrimSpace(name+" "+strings.Join(args, " ")) != "apt-get purge -y docker-compose-v2" {
				return "", errors.New("unexpected command")
			}
			packageInstalled = false
			return "removed", nil
		},
	}
	if _, err := uninstallDockerComposeWithOps(ops); err != nil {
		t.Fatal(err)
	}
	if _, owned := componentOwnership(dockerComposeComponentID); owned {
		t.Fatal("successful removal retained ownership")
	}

	if err := markComponentOwned(dockerComposeComponentID, map[string]string{dockerComposePackageName: "absent"}); err != nil {
		t.Fatal(err)
	}
	ops.run = func(string, ...string) (string, error) { return "failed", errors.New("apt failed") }
	if _, err := uninstallDockerComposeWithOps(ops); err == nil {
		t.Fatal("failed removal succeeded")
	}
	if _, owned := componentOwnership(dockerComposeComponentID); !owned {
		t.Fatal("failed removal cleared ownership")
	}
}

func TestRemoveExternalDockerComposePurgesOnlyVerifiedPackage(t *testing.T) {
	isolateComponentOwnership(t)
	packageInstalled := true
	var calls []string
	ops := dockerComposeOps{
		lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		installedPackages: func() (map[string]struct{}, error) {
			packages := map[string]struct{}{}
			if packageInstalled {
				packages[dockerComposePackageName] = struct{}{}
			}
			return packages, nil
		},
		run: func(name string, args ...string) (string, error) {
			signature := strings.TrimSpace(name + " " + strings.Join(args, " "))
			calls = append(calls, signature)
			switch signature {
			case "apt-get purge -y docker-compose-v2":
				packageInstalled = false
				return "removed", nil
			case "docker compose version --short":
				return "", errors.New("Compose unavailable")
			default:
				return "", errors.New("unexpected command: " + signature)
			}
		},
	}
	output, err := removeExternalDockerComposeWithOps(ops)
	if err != nil {
		t.Fatal(err)
	}
	if output != "External Docker Compose v2 package removed" || packageInstalled {
		t.Fatalf("output=%q packageInstalled=%v", output, packageInstalled)
	}
	if _, owned := componentOwnership(dockerComposeComponentID); owned {
		t.Fatal("external removal created ownership")
	}
	want := []string{"apt-get purge -y docker-compose-v2", "docker compose version --short"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v want=%#v", calls, want)
	}
}

func TestRemoveExternalDockerComposeRefusesManagedOrUnprovenPlugin(t *testing.T) {
	isolateComponentOwnership(t)
	noPackage := dockerComposeOps{
		installedPackages: func() (map[string]struct{}, error) { return map[string]struct{}{}, nil },
		run:               func(string, ...string) (string, error) { return "", errors.New("unexpected command") },
	}
	if _, err := removeExternalDockerComposeWithOps(noPackage); err == nil || !strings.Contains(err.Error(), "CLI plugins") {
		t.Fatalf("unproven plugin removal = %v", err)
	}
	if err := markComponentOwned(dockerComposeComponentID, map[string]string{dockerComposePackageName: "absent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := removeExternalDockerComposeWithOps(noPackage); err == nil || !strings.Contains(err.Error(), "managed by SBP") {
		t.Fatalf("managed external removal = %v", err)
	}
}

func TestRemoveExternalDockerComposeFailsClosedWhenOwnershipCannotBeRead(t *testing.T) {
	originalOwnershipPath := componentOwnershipPath
	componentOwnershipPath = filepath.Join(t.TempDir(), "ownership-directory")
	if err := os.Mkdir(componentOwnershipPath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { componentOwnershipPath = originalOwnershipPath })

	runs := 0
	ops := dockerComposeOps{
		installedPackages: func() (map[string]struct{}, error) {
			return map[string]struct{}{dockerComposePackageName: {}}, nil
		},
		run: func(string, ...string) (string, error) {
			runs++
			return "", nil
		},
	}
	if _, err := removeExternalDockerComposeWithOps(ops); err == nil || !strings.Contains(err.Error(), "inspect Docker Compose v2 ownership") {
		t.Fatalf("unreadable ownership removal = %v", err)
	}
	if runs != 0 {
		t.Fatalf("package removal ran with unreadable ownership: %d commands", runs)
	}
}

func TestDockerComponentCannotBeRemovedBeforeCompose(t *testing.T) {
	isolateComponentOwnership(t)
	if err := markComponentOwned("docker", nil); err != nil {
		t.Fatal(err)
	}
	if err := markComponentOwned(dockerComposeComponentID, map[string]string{dockerComposePackageName: "absent"}); err != nil {
		t.Fatal(err)
	}
	discovery := Discovery{
		DockerAvailable: true,
		DockerCompose:   DockerComposeState{Managed: true, Installed: true, CanUninstall: true},
		images:          map[string]bool{},
	}
	docker := componentState(t, componentStates(discovery, false), "docker")
	if docker.CanUninstall || !strings.Contains(docker.Note, "Compose") {
		t.Fatalf("Docker dependency state = %#v", docker)
	}
}

func TestDockerComponentRemovalFailsClosedWhenComposeStatusIsUnknown(t *testing.T) {
	isolateComponentOwnership(t)
	if err := markComponentOwned("docker", nil); err != nil {
		t.Fatal(err)
	}
	discovery := Discovery{
		DockerAvailable: true,
		DockerCompose:   DockerComposeState{InspectionFailed: true},
		images:          map[string]bool{},
	}
	docker := componentState(t, componentStates(discovery, false), "docker")
	if docker.CanUninstall || !strings.Contains(docker.Note, "could not be inspected") {
		t.Fatalf("Docker unknown Compose state = %#v", docker)
	}
}
