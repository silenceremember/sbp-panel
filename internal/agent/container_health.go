package agent

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type containerState struct {
	Running      bool
	Status       string
	RestartCount int
	ExitCode     int
}

func parseContainerState(value string) (containerState, error) {
	parts := strings.Split(strings.TrimSpace(value), "|")
	if len(parts) != 4 {
		return containerState{}, errors.New("Docker returned an invalid container state")
	}
	running, err := strconv.ParseBool(parts[0])
	if err != nil {
		return containerState{}, fmt.Errorf("invalid Docker running state: %w", err)
	}
	restarts, err := strconv.Atoi(parts[2])
	if err != nil {
		return containerState{}, fmt.Errorf("invalid Docker restart count: %w", err)
	}
	exitCode, err := strconv.Atoi(parts[3])
	if err != nil {
		return containerState{}, fmt.Errorf("invalid Docker exit code: %w", err)
	}
	return containerState{Running: running, Status: parts[1], RestartCount: restarts, ExitCode: exitCode}, nil
}

func inspectContainerState(name string) (containerState, error) {
	result := fixedCommand("docker", "inspect", "--format", "{{.State.Running}}|{{.State.Status}}|{{.RestartCount}}|{{.State.ExitCode}}", name)
	if !result.OK {
		return containerState{}, fmt.Errorf("could not inspect %s: %s", name, strings.TrimSpace(result.Error+" "+result.Output))
	}
	return parseContainerState(result.Output)
}

func waitContainerReady(name string, timeout time.Duration, probe func() error) error {
	deadline := time.Now().Add(timeout)
	var last containerState
	var lastProbe error
	stableSamples := 0
	stableRestarts := -1
	for time.Now().Before(deadline) {
		state, err := inspectContainerState(name)
		if err != nil {
			stableSamples = 0
		} else {
			last = state
			if state.Running {
				if state.RestartCount == stableRestarts {
					stableSamples++
				} else {
					stableRestarts = state.RestartCount
					stableSamples = 1
				}
				if probe != nil {
					lastProbe = probe()
					if lastProbe != nil {
						stableSamples = 0
					}
				}
				if stableSamples >= 2 {
					return nil
				}
			} else {
				stableSamples = 0
				stableRestarts = state.RestartCount
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if lastProbe != nil {
		return fmt.Errorf("%s started but did not become ready: %w", name, lastProbe)
	}
	return fmt.Errorf("%s did not stay running (status=%s, restarts=%d, exit=%d)", name, last.Status, last.RestartCount, last.ExitCode)
}
