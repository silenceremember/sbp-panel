package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/silenceremember/sbp-panel/internal/config"
)

const updateClientTimeout = 15 * time.Minute

func updateSocketClient(socket string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func updateClientRequest(client *http.Client, baseURL, method, path string) (UpdateProgress, error) {
	req, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return UpdateProgress{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return UpdateProgress{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return UpdateProgress{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var result struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(body, &result)
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = strings.TrimSpace(result.Error)
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return UpdateProgress{}, errors.New(message)
	}
	var progress UpdateProgress
	if err := json.Unmarshal(body, &progress); err != nil {
		return UpdateProgress{}, fmt.Errorf("invalid update response: %w", err)
	}
	return progress, nil
}

func printUpdateProgress(output io.Writer, progress UpdateProgress, previous string) string {
	line := fmt.Sprintf("[%3d%%] %s", progress.Progress, strings.TrimSpace(progress.Stage))
	if progress.Target != "" {
		line += " (v" + strings.TrimPrefix(progress.Target, "v") + ")"
	}
	if line != previous {
		if updateClientUsesColor(output) {
			color := "\x1b[38;2;239;155;71m"
			if progress.Status == "done" || progress.Progress >= 100 {
				color = "\x1b[38;2;72;211;143m"
			}
			fmt.Fprintf(output, "%s[%3d%%]\x1b[0m %s\n", color, progress.Progress, strings.TrimPrefix(line, fmt.Sprintf("[%3d%%] ", progress.Progress)))
		} else {
			fmt.Fprintln(output, line)
		}
	}
	return line
}

func updateClientUsesColor(output io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runUpdateClient(client *http.Client, baseURL string, output io.Writer, pollInterval time.Duration, deadline time.Time) error {
	progress, err := updateClientRequest(client, baseURL, http.MethodPost, "/v1/update")
	if err != nil {
		return fmt.Errorf("start update: %w", err)
	}
	previous := printUpdateProgress(output, progress, "")
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		progress, err = updateClientRequest(client, baseURL, http.MethodGet, "/v1/update/progress")
		if err != nil {
			// The Unix socket disappears briefly while the services restart.
			continue
		}
		previous = printUpdateProgress(output, progress, previous)
		switch progress.Status {
		case "done":
			return nil
		case "error":
			if progress.Error == "" {
				progress.Error = "update failed"
			}
			return errors.New(progress.Error)
		}
	}
	return errors.New("update timed out after 15 minutes")
}

// RunUpdateClient updates SBP through the already running privileged agent.
// It is used by the sbp-panel-update command and does not depend on the web UI.
func RunUpdateClient(configPath string, output io.Writer) error {
	c, err := config.Load(configPath)
	if err != nil {
		return err
	}
	return runUpdateClient(updateSocketClient(c.AgentSocket), "http://unix", output, time.Second, time.Now().Add(updateClientTimeout))
}
