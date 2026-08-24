package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/silenceremember/sbp-panel/internal/config"
)

const (
	dockerComposeComponentID  = "docker-compose-v2"
	dockerComposePackageName  = "docker-compose-v2"
	dockerComposeCommandLimit = 15 * time.Minute
)

type DockerComposeState struct {
	Installed         bool   `json:"installed"`
	Managed           bool   `json:"managed"`
	External          bool   `json:"external"`
	InspectionFailed  bool   `json:"inspection_failed"`
	Version           string `json:"version,omitempty"`
	CanInstall        bool   `json:"can_install"`
	CanUninstall      bool   `json:"can_uninstall"`
	CanRemoveExternal bool   `json:"can_remove_external"`
	Note              string `json:"note,omitempty"`
}

type dockerComposeOps struct {
	lookPath          func(string) (string, error)
	installedPackages func() (map[string]struct{}, error)
	run               func(string, ...string) (string, error)
}

func defaultDockerComposeOps() dockerComposeOps {
	return dockerComposeOps{
		lookPath:          exec.LookPath,
		installedPackages: installedDPKGPackages,
		run:               runDockerComposeCommand,
	}
}

func runDockerComposeCommand(name string, args ...string) (string, error) {
	limit := dockerComposeCommandLimit
	if name == "docker" {
		limit = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	body, err := command.CombinedOutput()
	if len(body) > 32<<10 {
		body = body[len(body)-(32<<10):]
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(body), fmt.Errorf("%s timed out after %s", name, limit)
	}
	if err != nil {
		return string(body), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

func dockerComposeStatus(dockerAvailable bool) DockerComposeState {
	return inspectDockerCompose(dockerAvailable, defaultDockerComposeOps())
}

func inspectDockerCompose(dockerAvailable bool, ops dockerComposeOps) DockerComposeState {
	state := DockerComposeState{}
	_, state.Managed = componentOwnership(dockerComposeComponentID)
	state.CanUninstall = state.Managed

	packages, err := ops.installedPackages()
	if err != nil {
		state.InspectionFailed = true
		state.CanUninstall = false
		state.Note = "Docker Compose v2 package status could not be inspected."
		return state
	}
	_, packageInstalled := packages[dockerComposePackageName]
	version := ""
	working := false
	if dockerAvailable {
		if output, versionErr := ops.run("docker", "compose", "version", "--short"); versionErr == nil {
			version = strings.TrimSpace(output)
			working = version != ""
		}
	}

	state.Installed = packageInstalled && working
	state.Version = version
	present := packageInstalled || working
	state.External = present && !state.Managed
	state.CanInstall = dockerAvailable && ((state.Managed && !state.Installed) || (!state.Managed && !present))
	state.CanRemoveExternal = state.External && packageInstalled

	switch {
	case state.External && packageInstalled && !working:
		state.Note = "The external Ubuntu Docker Compose v2 package was detected, but its command could not be validated. Only that exact package can be removed."
	case state.External && packageInstalled:
		state.Note = "The external Ubuntu Docker Compose v2 package was detected. It can be removed without adopting it."
	case state.External:
		state.Note = "An external Docker Compose CLI plugin was detected without the Ubuntu package. SBP cannot remove it safely."
	case state.Managed && !dockerAvailable:
		state.Note = "Docker is unavailable. Remove Compose v2 or restore Docker before repairing it."
	case state.Managed && !state.Installed:
		state.Note = "The managed Docker Compose v2 package is incomplete. Install repairs it; Remove clears the managed package."
	case !dockerAvailable:
		state.Note = "Install Docker before installing Docker Compose v2."
	}
	return state
}

func installDockerCompose() (string, error) {
	return installDockerComposeWithOps(defaultDockerComposeOps())
}

func installDockerComposeWithOps(ops dockerComposeOps) (string, error) {
	if _, err := ops.lookPath("docker"); err != nil {
		return "", errors.New("install Docker before installing Docker Compose v2")
	}
	packages, err := ops.installedPackages()
	if err != nil {
		return "", fmt.Errorf("inspect Docker Compose v2 package: %w", err)
	}
	_, packageInstalled := packages[dockerComposePackageName]
	owned, alreadyOwned := componentOwnership(dockerComposeComponentID)
	versionOutput, versionErr := ops.run("docker", "compose", "version", "--short")
	working := versionErr == nil && strings.TrimSpace(versionOutput) != ""
	if !alreadyOwned && (packageInstalled || working) {
		return "", errors.New("Docker Compose v2 is already installed outside SBP; it was not changed or adopted")
	}
	if alreadyOwned && packageInstalled && working {
		return "Docker Compose v2 is already managed by SBP (" + strings.TrimSpace(versionOutput) + ")", nil
	}

	previous := owned.Previous
	if !alreadyOwned {
		previous = map[string]string{dockerComposePackageName: "absent"}
	}
	if _, err := ops.run("apt-get", "update"); err != nil {
		return "", err
	}
	out, err := ops.run("apt-get", "install", "-y", "--no-install-recommends", dockerComposePackageName)
	if err != nil {
		if !alreadyOwned {
			if rollbackErr := rollbackDockerComposeInstall(previous, ops); rollbackErr != nil {
				return out, fmt.Errorf("install Docker Compose v2: %w (rollback also failed: %v)", err, rollbackErr)
			}
		}
		return out, err
	}
	versionOutput, err = ops.run("docker", "compose", "version", "--short")
	if err != nil || strings.TrimSpace(versionOutput) == "" {
		validationErr := errors.New("docker compose version validation failed after package installation")
		if err != nil {
			validationErr = fmt.Errorf("validate Docker Compose v2: %w", err)
		}
		if !alreadyOwned {
			if rollbackErr := rollbackDockerComposeInstall(previous, ops); rollbackErr != nil {
				return out, fmt.Errorf("%w (rollback also failed: %v)", validationErr, rollbackErr)
			}
		}
		return out, validationErr
	}
	if !alreadyOwned {
		if err := markComponentOwned(dockerComposeComponentID, previous); err != nil {
			rollbackErr := rollbackDockerComposeInstall(previous, ops)
			if rollbackErr != nil {
				return out, fmt.Errorf("Docker Compose v2 ownership could not be recorded: %w (rollback also failed: %v)", err, rollbackErr)
			}
			return out, fmt.Errorf("Docker Compose v2 installation was rolled back because ownership could not be recorded: %w", err)
		}
	}
	return "Docker Compose v2 " + strings.TrimSpace(versionOutput) + " installed", nil
}

func rollbackDockerComposeInstall(previous map[string]string, ops dockerComposeOps) error {
	if previous[dockerComposePackageName] != "absent" {
		return errors.New("Docker Compose v2 rollback is not proven safe")
	}
	if _, err := ops.run("apt-get", "purge", "-y", dockerComposePackageName); err != nil {
		return err
	}
	packages, err := ops.installedPackages()
	if err != nil {
		return fmt.Errorf("verify Docker Compose v2 rollback: %w", err)
	}
	if _, installed := packages[dockerComposePackageName]; installed {
		return errors.New("Docker Compose v2 package remained after rollback")
	}
	return nil
}

func uninstallDockerCompose() (string, error) {
	return uninstallDockerComposeWithOps(defaultDockerComposeOps())
}

func uninstallDockerComposeWithOps(ops dockerComposeOps) (string, error) {
	owned, ok := componentOwnership(dockerComposeComponentID)
	if !ok {
		return "", errors.New("Docker Compose v2 was not installed by SBP and will not be removed")
	}
	if owned.Previous[dockerComposePackageName] != "absent" {
		return "", errors.New("Docker Compose v2 ownership does not prove that the package can be removed")
	}
	out, err := ops.run("apt-get", "purge", "-y", dockerComposePackageName)
	if err != nil {
		return out, err
	}
	packages, err := ops.installedPackages()
	if err != nil {
		return out, fmt.Errorf("verify Docker Compose v2 removal: %w", err)
	}
	if _, installed := packages[dockerComposePackageName]; installed {
		return out, errors.New("Docker Compose v2 package remained after removal")
	}
	if err := clearComponentOwnership(dockerComposeComponentID); err != nil {
		return out, fmt.Errorf("clear Docker Compose v2 ownership: %w", err)
	}
	return "Docker Compose v2 removed", nil
}

func removeExternalDockerCompose() (string, error) {
	return removeExternalDockerComposeWithOps(defaultDockerComposeOps())
}

func removeExternalDockerComposeWithOps(ops dockerComposeOps) (string, error) {
	if _, owned, err := checkedComponentOwnership(dockerComposeComponentID); err != nil {
		return "", fmt.Errorf("inspect Docker Compose v2 ownership before external removal: %w", err)
	} else if owned {
		return "", errors.New("Docker Compose v2 is managed by SBP; use ordinary removal")
	}
	packages, err := ops.installedPackages()
	if err != nil {
		return "", fmt.Errorf("inspect external Docker Compose v2 package: %w", err)
	}
	if _, installed := packages[dockerComposePackageName]; !installed {
		return "", errors.New("the external Ubuntu docker-compose-v2 package is not installed; CLI plugins are not removed automatically")
	}
	out, err := ops.run("apt-get", "purge", "-y", dockerComposePackageName)
	if err != nil {
		return out, fmt.Errorf("remove external Docker Compose v2 package: %w", err)
	}
	remaining, err := ops.installedPackages()
	if err != nil {
		return out, fmt.Errorf("verify external Docker Compose v2 removal: %w", err)
	}
	if _, installed := remaining[dockerComposePackageName]; installed {
		return out, errors.New("external Docker Compose v2 package remained after removal")
	}
	if ops.lookPath != nil {
		if _, dockerErr := ops.lookPath("docker"); dockerErr == nil {
			if version, versionErr := ops.run("docker", "compose", "version", "--short"); versionErr == nil && strings.TrimSpace(version) != "" {
				return "External Docker Compose v2 package removed. A separate external CLI plugin remains and was not changed.", nil
			}
		}
	}
	return "External Docker Compose v2 package removed", nil
}

func requireDockerComposeAbsent() error {
	if _, owned := componentOwnership(dockerComposeComponentID); owned {
		return errors.New("remove SBP-managed Docker Compose v2 in Docker settings first")
	}
	packages, err := installedDPKGPackages()
	if err != nil {
		return fmt.Errorf("inspect Docker Compose v2 before removing Docker: %w", err)
	}
	if _, installed := packages[dockerComposePackageName]; installed {
		return errors.New("remove external Docker Compose v2 before removing Docker")
	}
	if _, err := exec.LookPath("docker"); err == nil {
		if output, versionErr := run("docker", "compose", "version", "--short"); versionErr == nil && strings.TrimSpace(output) != "" {
			return errors.New("remove the external Docker Compose CLI plugin before removing Docker")
		}
	}
	return nil
}

func (i *installer) startDockerComposeInstall() error {
	return i.startJob("docker", "compose-install", func(string, config.Config) (string, error) {
		return installDockerCompose()
	})
}

func (i *installer) startDockerComposeUninstall() error {
	return i.startJob("docker", "compose-uninstall", func(string, config.Config) (string, error) {
		return uninstallDockerCompose()
	})
}

func (i *installer) startDockerComposeExternalRemoval() error {
	return i.startJob("docker", "compose-external-remove", func(string, config.Config) (string, error) {
		return removeExternalDockerCompose()
	})
}
