package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/silenceremember/sbp-panel/internal/buildinfo"
	"github.com/silenceremember/sbp-panel/internal/config"
)

const (
	maxUpdateBytes      = 128 << 20
	maxReleaseListBytes = 2 << 20
	maxReleaseCount     = 100
)

const (
	defaultUpdateConfigPath = "/etc/vpn-panel/config.json"
	updateWatchdogUnit      = "sbp-panel-update-watchdog"
)

var (
	updateMu                    sync.Mutex
	updateJobMu                 sync.Mutex
	updateJobRunning            bool
	updateProgressPath          = "/run/vpn-panel/update-progress.json"
	updateTransactionPath       = "/var/lib/vpn-panel-agent/update-transaction.json"
	updateBinaryPath            = "/opt/vpn-panel/bin/vpn-panel"
	updateStagingRoot           = "/opt/vpn-panel"
	updateHealthTimeout         = 45 * time.Second
	updateHealthInterval        = time.Second
	updateRequiredHealthyChecks = 2
	updateCommand               = run
	updateHealthProbe           = probeUpdateHealth
	updateWatchdogScheduler     = scheduleUpdateWatchdog
)

type updateTransaction struct {
	Target               string `json:"target"`
	Phase                string `json:"phase"`
	Failure              string `json:"failure,omitempty"`
	ConfigPath           string `json:"config_path"`
	BinaryPath           string `json:"binary_path"`
	BinaryRollbackPath   string `json:"binary_rollback_path"`
	PreviousBinarySHA256 string `json:"previous_binary_sha256"`
	NewBinarySHA256      string `json:"new_binary_sha256"`
	CreatedAt            string `json:"created_at"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type updateMetadata struct {
	Version   string `json:"version"`
	AssetName string `json:"asset_name"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Assets  []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

type UpdateInfo struct {
	OK                 bool   `json:"ok"`
	CurrentVersion     string `json:"current_version"`
	LatestVersion      string `json:"latest_version"`
	UpdateAvailable    bool   `json:"update_available"`
	IncludePrereleases bool   `json:"include_prereleases"`
	RepositoryURL      string `json:"repository_url"`
	ReleaseURL         string `json:"release_url,omitempty"`
	AssetName          string `json:"asset_name,omitempty"`
	Message            string `json:"message,omitempty"`
	asset              releaseAsset
}

type UpdateProgress struct {
	OK        bool   `json:"ok"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	Stage     string `json:"stage"`
	Target    string `json:"target,omitempty"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

func idleUpdateProgress() UpdateProgress {
	return UpdateProgress{OK: true, Status: "idle", Progress: 0, Stage: "No update is running."}
}

func readUpdateProgress() UpdateProgress {
	body, err := os.ReadFile(updateProgressPath)
	if err != nil {
		return idleUpdateProgress()
	}
	var progress UpdateProgress
	if json.Unmarshal(body, &progress) != nil || progress.Status == "" {
		return idleUpdateProgress()
	}
	progress.OK = progress.Status != "error"
	return progress
}

func writeUpdateProgress(progress UpdateProgress) {
	progress.OK = progress.Status != "error"
	progress.Progress = max(0, min(100, progress.Progress))
	progress.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(updateProgressPath), 0750); err != nil {
		return
	}
	body, err := json.Marshal(progress)
	if err != nil {
		return
	}
	temporary := updateProgressPath + ".tmp"
	if os.WriteFile(temporary, body, 0640) == nil {
		_ = os.Rename(temporary, updateProgressPath)
	}
}

func reconcileUpdateProgress() {
	_ = os.Remove(updateProgressPath + ".tmp")
	_ = os.Remove(updateTransactionPath + ".tmp")
	progress := readUpdateProgress()
	transaction, err := readUpdateTransaction()
	if err == nil {
		if progress.Target == "" {
			progress.Target = transaction.Target
		}
		progress.Status = "restarting"
		progress.Progress = max(progress.Progress, 97)
		progress.Stage = "Verifying the restarted services."
		progress.Error = ""
		writeUpdateProgress(progress)
		if err := updateWatchdogScheduler(transaction.ConfigPath); err != nil {
			writeUpdateProgress(UpdateProgress{
				Status:   "error",
				Progress: progress.Progress,
				Stage:    "Update recovery is waiting.",
				Target:   transaction.Target,
				Error:    "The rollback files were preserved, but the update watchdog could not be started: " + err.Error(),
			})
		}
		return
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeUpdateProgress(UpdateProgress{Status: "error", Progress: progress.Progress, Stage: "Update recovery failed.", Target: progress.Target, Error: err.Error()})
		return
	}
	if progress.Status != "running" && progress.Status != "restarting" {
		return
	}
	if progress.Target != "" && progress.Target == buildinfo.Version {
		writeUpdateProgress(UpdateProgress{Status: "done", Progress: 100, Stage: "Update installed.", Target: progress.Target})
		return
	}
	writeUpdateProgress(UpdateProgress{Status: "error", Progress: progress.Progress, Stage: "Update interrupted.", Target: progress.Target, Error: "The update was interrupted before the new version started. You can safely try again."})
}

func cleanupUpdateStaging() error {
	entries, err := os.ReadDir(updateStagingRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect update staging directory: %w", err)
	}
	pattern := regexp.MustCompile(`^\.update-[0-9]+$`)
	for _, entry := range entries {
		if !entry.IsDir() || !pattern.MatchString(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(updateStagingRoot, entry.Name())); err != nil {
			return fmt.Errorf("remove interrupted update staging directory %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func readUpdateTransaction() (updateTransaction, error) {
	body, err := os.ReadFile(updateTransactionPath)
	if err != nil {
		return updateTransaction{}, err
	}
	var transaction updateTransaction
	if err := json.Unmarshal(body, &transaction); err != nil {
		return updateTransaction{}, fmt.Errorf("invalid update recovery state: %w", err)
	}
	if transaction.Target == "" || transaction.BinaryPath != updateBinaryPath || transaction.BinaryRollbackPath != updateBinaryPath+".update-rollback" {
		return updateTransaction{}, errors.New("invalid update recovery state: required paths are missing")
	}
	if transaction.ConfigPath == "" {
		transaction.ConfigPath = defaultUpdateConfigPath
	}
	return transaction, nil
}

func writeUpdateTransaction(transaction updateTransaction) error {
	if transaction.CreatedAt == "" {
		transaction.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := os.MkdirAll(filepath.Dir(updateTransactionPath), 0750); err != nil {
		return err
	}
	body, err := json.Marshal(transaction)
	if err != nil {
		return err
	}
	temporary := updateTransactionPath + ".tmp"
	if err := os.WriteFile(temporary, body, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, updateTransactionPath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func scheduleUpdateWatchdog(configPath string) error {
	if configPath == "" {
		configPath = defaultUpdateConfigPath
	}
	for _, unit := range []string{updateWatchdogUnit + ".service", updateWatchdogUnit + ".timer"} {
		if _, err := updateCommand("systemctl", "is-active", "--quiet", unit); err == nil {
			return nil
		}
	}
	_, _ = updateCommand("systemctl", "reset-failed", updateWatchdogUnit+".service", updateWatchdogUnit+".timer")
	_, err := updateCommand(
		"systemd-run",
		"--collect",
		"--unit="+updateWatchdogUnit,
		"--on-active=2s",
		"--property=StandardOutput=null",
		"--property=StandardError=null",
		updateBinaryPath,
		"-mode", "update-watchdog",
		"-config", configPath,
	)
	return err
}

// RunUpdateWatchdog finishes the second phase of an update outside the panel
// and agent service cgroups, so it remains alive while both services restart.
func RunUpdateWatchdog(configPath string) error {
	c, err := config.Load(configPath)
	if err != nil {
		return err
	}
	transaction, err := readUpdateTransaction()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if transaction.Phase == "committing" {
		return commitUpdate(transaction)
	}
	if transaction.Phase == "rolled-back" {
		return finishRolledBackUpdate(transaction)
	}
	if transaction.Phase == "rolling-back" || transaction.Target != buildinfo.Version {
		if transaction.Failure == "" {
			transaction.Failure = "The update was interrupted before the target version started."
		}
		return rollbackUpdate(c, transaction)
	}

	writeUpdateProgress(UpdateProgress{Status: "restarting", Progress: 97, Stage: "Restarting and checking both services.", Target: transaction.Target})
	if _, err := updateCommand("systemctl", "restart", "vpn-panel-agent.service", "vpn-panel.service"); err != nil {
		transaction.Failure = "The updated services could not be restarted: " + err.Error()
		return rollbackUpdate(c, transaction)
	}
	if err := waitForUpdateHealth(c); err != nil {
		transaction.Failure = "The updated services did not become healthy: " + err.Error()
		return rollbackUpdate(c, transaction)
	}
	return commitUpdate(transaction)
}

func commitUpdate(transaction updateTransaction) error {
	transaction.Phase = "committing"
	if err := writeUpdateTransaction(transaction); err != nil {
		return err
	}
	if err := removeRollbackArtifacts(transaction); err != nil {
		writeUpdateProgress(UpdateProgress{Status: "error", Progress: 99, Stage: "Update cleanup is waiting.", Target: transaction.Target, Error: err.Error()})
		return err
	}
	if err := os.Remove(updateTransactionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	writeUpdateProgress(UpdateProgress{Status: "done", Progress: 100, Stage: "Update installed and verified.", Target: transaction.Target})
	return nil
}

func rollbackUpdate(c config.Config, transaction updateTransaction) error {
	transaction.Phase = "rolling-back"
	if err := writeUpdateTransaction(transaction); err != nil {
		return err
	}
	writeUpdateProgress(UpdateProgress{Status: "restarting", Progress: 98, Stage: "Restoring the previous version.", Target: transaction.Target})
	_, _ = updateCommand("systemctl", "stop", "vpn-panel.service", "vpn-panel-agent.service")
	if err := rollbackUpdateFiles(transaction); err != nil {
		message := "Automatic rollback could not restore all files; the recovery files were preserved: " + err.Error()
		writeUpdateProgress(UpdateProgress{Status: "error", Progress: 98, Stage: "Update rollback needs attention.", Target: transaction.Target, Error: message})
		return errors.New(message)
	}
	if _, err := updateCommand("systemctl", "restart", "vpn-panel-agent.service", "vpn-panel.service"); err != nil {
		message := "The previous files were restored, but their services could not be restarted; recovery files were preserved: " + err.Error()
		writeUpdateProgress(UpdateProgress{Status: "error", Progress: 98, Stage: "Update rolled back; services need attention.", Target: transaction.Target, Error: message})
		return errors.New(message)
	}
	if err := waitForUpdateHealth(c); err != nil {
		message := "The previous files were restored, but both services did not become healthy; recovery files were preserved: " + err.Error()
		writeUpdateProgress(UpdateProgress{Status: "error", Progress: 98, Stage: "Update rolled back; services need attention.", Target: transaction.Target, Error: message})
		return errors.New(message)
	}
	transaction.Phase = "rolled-back"
	if err := writeUpdateTransaction(transaction); err != nil {
		return err
	}
	return finishRolledBackUpdate(transaction)
}

func finishRolledBackUpdate(transaction updateTransaction) error {
	failure := strings.TrimSpace(transaction.Failure)
	if failure == "" {
		failure = "The update failed its health check."
	}
	if err := removeRollbackArtifacts(transaction); err != nil {
		return err
	}
	if err := os.Remove(updateTransactionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	writeUpdateProgress(UpdateProgress{Status: "error", Progress: 100, Stage: "Update rolled back safely.", Target: transaction.Target, Error: failure})
	return nil
}

func waitForUpdateHealth(c config.Config) error {
	deadline := time.Now().Add(updateHealthTimeout)
	consecutive := 0
	var lastErr error
	for {
		if err := updateHealthProbe(c); err != nil {
			lastErr = err
			consecutive = 0
		} else {
			consecutive++
			if consecutive >= max(1, updateRequiredHealthyChecks) {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			if lastErr == nil {
				lastErr = errors.New("health confirmation timed out")
			}
			return lastErr
		}
		time.Sleep(updateHealthInterval)
	}
}

func probeUpdateHealth(c config.Config) error {
	for _, service := range []string{"vpn-panel-agent.service", "vpn-panel.service"} {
		if _, err := updateCommand("systemctl", "is-active", "--quiet", service); err != nil {
			return fmt.Errorf("%s is not active", service)
		}
	}

	agentTransport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", c.AgentSocket)
	}}
	agentClient := &http.Client{Transport: agentTransport, Timeout: 3 * time.Second}
	if err := requireHealthyResponse(agentClient, "http://unix/v1/health"); err != nil {
		return fmt.Errorf("agent health check failed: %w", err)
	}

	// The request never leaves the host. Certificate verification is skipped
	// because bootstrap installations intentionally use a self-signed cert.
	panelTransport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	panelClient := &http.Client{Transport: panelTransport, Timeout: 3 * time.Second}
	if err := requireHealthyResponse(panelClient, localPanelURL(c.Listen)+"/api/bootstrap/status"); err != nil {
		return fmt.Errorf("panel health check failed: %w", err)
	}
	return nil
}

func requireHealthyResponse(client *http.Client, endpoint string) error {
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return nil
}

func localPanelURL(listen string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "https://127.0.0.1:9443"
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if host == "::" || host == "[::]" {
		host = "::1"
	}
	return "https://" + net.JoinHostPort(host, port)
}

func startUpdateJob(c config.Config, client *http.Client, includePrereleases bool) (UpdateProgress, error) {
	updateJobMu.Lock()
	if updateJobRunning {
		updateJobMu.Unlock()
		return readUpdateProgress(), nil
	}
	current := readUpdateProgress()
	if current.Status == "running" || current.Status == "restarting" {
		updateJobMu.Unlock()
		return current, nil
	}
	const lifecycleOwner = "panel-update"
	if err := acquireLifecycle(lifecycleOwner); err != nil {
		updateJobMu.Unlock()
		return UpdateProgress{}, err
	}
	updateJobRunning = true
	updateJobMu.Unlock()
	progress := UpdateProgress{Status: "running", Progress: 2, Stage: "Preparing update."}
	writeUpdateProgress(progress)
	go func() {
		defer func() {
			updateJobMu.Lock()
			updateJobRunning = false
			updateJobMu.Unlock()
			releaseLifecycle(lifecycleOwner)
		}()
		info, err := applyUpdateWithProgress(c, client, includePrereleases, func(next UpdateProgress) {
			writeUpdateProgress(next)
		})
		if err != nil {
			failed := readUpdateProgress()
			failed.Status = "error"
			failed.Stage = "Update failed."
			failed.Error = err.Error()
			writeUpdateProgress(failed)
			return
		}
		if !info.UpdateAvailable {
			writeUpdateProgress(UpdateProgress{Status: "done", Progress: 100, Stage: "The latest available version is already installed.", Target: info.LatestVersion})
			return
		}
		writeUpdateProgress(UpdateProgress{Status: "restarting", Progress: 96, Stage: "Restarting services.", Target: info.LatestVersion})
	}()
	return progress, nil
}

func latestUpdateMetadataURL() string {
	return "https://github.com/" + buildinfo.Repository + "/releases/latest/download/sbp-panel-update.json"
}

func releasesAPIURL() string {
	return "https://api.github.com/repos/" + buildinfo.Repository + "/releases?per_page=100"
}

func releaseDownloadBaseURL() string {
	return "https://github.com/" + buildinfo.Repository + "/releases/download"
}

func includePrereleasesFromQuery(rawQuery string) (bool, error) {
	if rawQuery == "" {
		return false, nil
	}
	if rawQuery != "include_prereleases=1" {
		return false, errors.New("invalid update channel")
	}
	return true, nil
}

func checkUpdate(client *http.Client, includePrereleases bool) (UpdateInfo, error) {
	return checkUpdateWithURLs(client, includePrereleases, latestUpdateMetadataURL(), releasesAPIURL(), releaseDownloadBaseURL())
}

func checkUpdateWithURLs(client *http.Client, includePrereleases bool, stableMetadataURL, releaseListURL, downloadBaseURL string) (UpdateInfo, error) {
	metadataURL := stableMetadataURL
	expectedVersion := ""
	if includePrereleases {
		var err error
		metadataURL, expectedVersion, err = latestReleaseMetadataURL(client, releaseListURL, downloadBaseURL)
		if err != nil {
			return UpdateInfo{OK: true, CurrentVersion: buildinfo.Version, IncludePrereleases: true, RepositoryURL: buildinfo.RepositoryURL()}, err
		}
	}
	info, err := checkUpdateAt(client, metadataURL, includePrereleases)
	if err == nil && expectedVersion != "" && info.LatestVersion != expectedVersion {
		return info, fmt.Errorf("release metadata version %q does not match tag v%s", info.LatestVersion, expectedVersion)
	}
	return info, err
}

func latestReleaseMetadataURL(client *http.Client, releaseListURL, downloadBaseURL string) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, releaseListURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", buildinfo.Name+"/"+buildinfo.Version)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GitHub releases are unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub releases returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseListBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("failed to read GitHub releases: %w", err)
	}
	if len(body) > maxReleaseListBytes {
		return "", "", errors.New("GitHub release list is too large")
	}
	var releases []githubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return "", "", fmt.Errorf("invalid GitHub release list: %w", err)
	}
	if len(releases) > maxReleaseCount {
		return "", "", errors.New("GitHub returned too many releases")
	}
	best := ""
	for _, release := range releases {
		version := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
		if _, err := parseVersion(version); err != nil || release.Draft || release.TagName != "v"+version {
			continue
		}
		metadataAssets := 0
		for _, asset := range release.Assets {
			if asset.Name == "sbp-panel-update.json" {
				metadataAssets++
			}
		}
		if metadataAssets != 1 || best != "" && compareVersions(version, best) <= 0 {
			continue
		}
		best = version
	}
	if best == "" {
		return "", "", errors.New("GitHub has no supported release with update metadata")
	}
	return strings.TrimRight(downloadBaseURL, "/") + "/v" + best + "/sbp-panel-update.json", best, nil
}

func checkUpdateAt(client *http.Client, metadataURL string, includePrereleases bool) (UpdateInfo, error) {
	info := UpdateInfo{OK: true, CurrentVersion: buildinfo.Version, IncludePrereleases: includePrereleases, RepositoryURL: buildinfo.RepositoryURL()}
	req, err := http.NewRequest(http.MethodGet, metadataURL, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", buildinfo.Name+"/"+buildinfo.Version)
	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf("GitHub is unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("GitHub update metadata returned HTTP %d", resp.StatusCode)
	}
	var metadata updateMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&metadata); err != nil {
		return info, fmt.Errorf("invalid GitHub update metadata: %w", err)
	}
	latest := strings.TrimPrefix(strings.TrimSpace(metadata.Version), "v")
	if _, err := parseVersion(latest); err != nil {
		return info, fmt.Errorf("invalid release version %q", metadata.Version)
	}
	info.LatestVersion = latest
	info.ReleaseURL = buildinfo.RepositoryURL() + "/releases/tag/v" + latest
	info.UpdateAvailable = compareVersions(latest, buildinfo.Version) > 0
	if !info.UpdateAvailable {
		if includePrereleases {
			info.Message = "The latest stable or prerelease version is installed."
		} else {
			info.Message = "The latest stable version is installed."
		}
		return info, nil
	}
	wanted := "sbp-panel-" + latest + "-linux-amd64.zip"
	if metadata.AssetName != wanted {
		return info, fmt.Errorf("the release metadata does not reference %s", wanted)
	}
	if metadata.Size <= 0 || metadata.Size > maxUpdateBytes {
		return info, errors.New("invalid update archive size")
	}
	asset := releaseAsset{
		Name:               metadata.AssetName,
		BrowserDownloadURL: buildinfo.RepositoryURL() + "/releases/download/v" + latest + "/" + metadata.AssetName,
		Digest:             "sha256:" + strings.TrimSpace(metadata.SHA256),
		Size:               metadata.Size,
	}
	if err := validateDigest(asset.Digest); err != nil {
		return info, fmt.Errorf("the release cannot be installed safely: %w", err)
	}
	info.AssetName = asset.Name
	info.asset = asset
	info.Message = "Version v" + latest + "."
	return info, nil
}

type parsedVersion [3]int

func parseVersion(value string) (parsedVersion, error) {
	var result parsedVersion
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return result, errors.New("expected major.minor.patch")
	}
	for i, part := range parts {
		if part == "" || strings.ContainsAny(part, "+-") {
			return result, errors.New("numeric version required")
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return result, errors.New("numeric version required")
		}
		result[i] = n
	}
	return result, nil
}

func compareVersions(a, b string) int {
	av, ae := parseVersion(a)
	bv, be := parseVersion(b)
	if ae != nil || be != nil {
		return strings.Compare(a, b)
	}
	for i := range av {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}

var digestPattern = regexp.MustCompile(`^sha256:([0-9a-fA-F]{64})$`)

func validateDigest(value string) error {
	if !digestPattern.MatchString(value) {
		return errors.New("GitHub did not provide a SHA-256 digest")
	}
	return nil
}

func applyUpdateWithProgress(c config.Config, client *http.Client, includePrereleases bool, report func(UpdateProgress)) (UpdateInfo, error) {
	updateMu.Lock()
	defer updateMu.Unlock()

	updateReport(report, 8, "Checking the latest release.", "")
	info, err := checkUpdate(client, includePrereleases)
	if err != nil || !info.UpdateAvailable {
		return info, err
	}
	updateReport(report, 14, "Starting the download.", info.LatestVersion)
	assetURL, err := url.Parse(info.asset.BrowserDownloadURL)
	if err != nil || assetURL.Scheme != "https" || assetURL.Host != "github.com" {
		return info, errors.New("GitHub returned an invalid archive URL")
	}
	req, _ := http.NewRequest(http.MethodGet, assetURL.String(), nil)
	req.Header.Set("User-Agent", buildinfo.Name+"/"+buildinfo.Version)
	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf("failed to download the update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("update download returned HTTP %d", resp.StatusCode)
	}
	body, err := readUpdateArchive(resp.Body, info.asset.Size, func(percent int) {
		updateReport(report, percent, "Downloading the update.", info.LatestVersion)
	})
	if err != nil || len(body) > maxUpdateBytes || int64(len(body)) != info.asset.Size {
		return info, errors.New("the update archive is too large or corrupted")
	}
	updateReport(report, 62, "Verifying the download.", info.LatestVersion)
	expected, _ := hex.DecodeString(digestPattern.FindStringSubmatch(info.asset.Digest)[1])
	actual := sha256.Sum256(body)
	if !bytes.Equal(actual[:], expected) {
		return info, errors.New("archive SHA-256 mismatch; update cancelled")
	}
	if err := installUpdateArchiveWithProgress(c, body, info.LatestVersion, report); err != nil {
		return info, err
	}
	info.Message = "Simple Bridge Panel updated to v" + info.LatestVersion + ". Services are restarting."
	return info, nil
}

func updateReport(report func(UpdateProgress), progress int, stage, target string) {
	if report != nil {
		report(UpdateProgress{Status: "running", Progress: progress, Stage: stage, Target: target})
	}
}

func readUpdateArchive(reader io.Reader, size int64, report func(int)) ([]byte, error) {
	var body bytes.Buffer
	limited := io.LimitReader(reader, maxUpdateBytes+1)
	buffer := make([]byte, 64<<10)
	read := int64(0)
	lastProgress := -1
	for {
		n, err := limited.Read(buffer)
		if n > 0 {
			read += int64(n)
			_, _ = body.Write(buffer[:n])
			progress := 15
			if size > 0 {
				progress += int(min(int64(44), read*44/size))
			}
			if progress != lastProgress && report != nil {
				report(progress)
				lastProgress = progress
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if read > maxUpdateBytes || size <= 0 || read != size {
		return nil, errors.New("update archive size does not match release metadata")
	}
	return body.Bytes(), nil
}

func installUpdateArchiveWithProgress(c config.Config, archive []byte, target string, report func(UpdateProgress)) error {
	root, err := os.MkdirTemp(updateStagingRoot, ".update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	updateReport(report, 68, "Extracting the package.", target)
	binaryPath, err := extractUpdateArchive(archive, root)
	if err != nil {
		return err
	}
	updateReport(report, 76, "Checking the new build.", target)
	if out, err := exec.Command(binaryPath, "-mode", "self-check", "-config", defaultUpdateConfigPath).CombinedOutput(); err != nil {
		return fmt.Errorf("the new binary failed its self-check: %s", strings.TrimSpace(string(out)))
	}

	updateReport(report, 84, "Replacing the application files.", target)
	return installPreparedApplication(binaryPath, target, func() error {
		updateReport(report, 92, "Scheduling the service restart.", target)
		return nil
	})
}

func installPreparedApplication(binaryPath, target string, beforeSchedule func() error) error {
	if _, err := readUpdateTransaction(); err == nil {
		return errors.New("an earlier update still has recovery files; restart the panel to finish recovery before trying again")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	transaction := updateTransaction{
		Target:             target,
		Phase:              "prepared",
		ConfigPath:         defaultUpdateConfigPath,
		BinaryPath:         updateBinaryPath,
		BinaryRollbackPath: updateBinaryPath + ".update-rollback",
	}
	if target == "" {
		transaction.Target = buildinfo.Version
	}
	if updatePathExists(transaction.BinaryRollbackPath) {
		return errors.New("update recovery files already exist; restart the panel to reconcile them before trying again")
	}
	var err error
	transaction.PreviousBinarySHA256, err = fileSHA256(transaction.BinaryPath)
	if err != nil {
		return fmt.Errorf("failed to identify the working binary: %w", err)
	}
	transaction.NewBinarySHA256, err = fileSHA256(binaryPath)
	if err != nil {
		return fmt.Errorf("failed to identify the new binary: %w", err)
	}
	if err := writeUpdateTransaction(transaction); err != nil {
		return fmt.Errorf("failed to save update recovery state: %w", err)
	}
	prepared := false
	defer func() {
		if !prepared {
			_ = os.Remove(updateBinaryPath + ".next")
		}
	}()
	if err := copyFileAtomic(transaction.BinaryPath, transaction.BinaryRollbackPath, 0755); err != nil {
		_ = os.Remove(updateTransactionPath)
		return fmt.Errorf("failed to preserve the working binary: %w", err)
	}
	transaction.Phase = "backed-up"
	if err := writeUpdateTransaction(transaction); err != nil {
		if rollbackErr := rollbackPreparedUpdate(transaction); rollbackErr != nil {
			return fmt.Errorf("failed to record the recovery copy: %w (recovery also failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("failed to record the recovery copy: %w", err)
	}
	if err := copyFileAtomic(binaryPath, transaction.BinaryPath, 0755); err != nil {
		if rollbackErr := rollbackPreparedUpdate(transaction); rollbackErr != nil {
			return fmt.Errorf("failed to replace the binary: %w (recovery also failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("failed to replace the binary: %w", err)
	}
	transaction.Phase = "installed"
	if err := writeUpdateTransaction(transaction); err != nil {
		if rollbackErr := rollbackPreparedUpdate(transaction); rollbackErr != nil {
			return fmt.Errorf("failed to record the installed update: %w (recovery also failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("failed to record the installed update: %w", err)
	}
	prepared = true
	if beforeSchedule != nil {
		if err := beforeSchedule(); err != nil {
			if rollbackErr := rollbackPreparedUpdate(transaction); rollbackErr != nil {
				return fmt.Errorf("the application was prepared, but its completion state could not be saved: %w (recovery files were preserved because rollback also failed: %v)", err, rollbackErr)
			}
			return fmt.Errorf("the application was prepared, but its completion state could not be saved; the previous version was restored: %w", err)
		}
	}
	if err := updateWatchdogScheduler(defaultUpdateConfigPath); err != nil {
		if rollbackErr := rollbackPreparedUpdate(transaction); rollbackErr != nil {
			return fmt.Errorf("automatic restart could not be scheduled: %w (recovery files were preserved because rollback also failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("automatic restart could not be scheduled; the previous version was restored: %w", err)
	}
	return nil
}

func rollbackPreparedUpdate(transaction updateTransaction) error {
	if err := rollbackUpdateFiles(transaction); err != nil {
		return err
	}
	transaction.Phase = "rolled-back"
	if err := writeUpdateTransaction(transaction); err != nil {
		return err
	}
	return finishRolledBackUpdate(transaction)
}

func rollbackUpdateFiles(transaction updateTransaction) error {
	var problems []string
	rollbackHash, err := fileSHA256(transaction.BinaryRollbackPath)
	if err == nil {
		if rollbackHash != transaction.PreviousBinarySHA256 {
			problems = append(problems, "the preserved binary checksum does not match")
		} else if err := copyFileAtomic(transaction.BinaryRollbackPath, transaction.BinaryPath, 0755); err != nil {
			problems = append(problems, "failed to restore the binary: "+err.Error())
		} else if restoredHash, hashErr := fileSHA256(transaction.BinaryPath); hashErr != nil || restoredHash != transaction.PreviousBinarySHA256 {
			problems = append(problems, "the restored binary could not be verified")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		currentHash, currentErr := fileSHA256(transaction.BinaryPath)
		if currentErr != nil || currentHash != transaction.PreviousBinarySHA256 {
			problems = append(problems, "the preserved binary is missing and the active binary is not the previous build")
		}
	} else {
		problems = append(problems, "failed to read the preserved binary: "+err.Error())
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func removeRollbackArtifacts(transaction updateTransaction) error {
	var problems []string
	var paths []string
	if transaction.BinaryRollbackPath != "" {
		paths = append(paths, transaction.BinaryRollbackPath, transaction.BinaryRollbackPath+".next")
	}
	if transaction.BinaryPath != "" {
		paths = append(paths, transaction.BinaryPath+".next")
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			problems = append(problems, path+": "+err.Error())
		}
	}
	if len(problems) > 0 {
		return errors.New("failed to remove update recovery files: " + strings.Join(problems, "; "))
	}
	return nil
}

func updatePathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyFileAtomic(src, dst string, mode os.FileMode) error {
	temporary := dst + ".next"
	_ = os.Remove(temporary)
	if err := copyFile(src, temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, dst); err != nil {
		if runtime.GOOS == "windows" {
			if removeErr := os.Remove(dst); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				err = os.Rename(temporary, dst)
			}
		}
		if err == nil {
			return nil
		}
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func extractUpdateArchive(archive []byte, root string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return "", fmt.Errorf("the update archive is corrupted: %w", err)
	}
	binaryPath := filepath.Join(root, "sbp-panel-linux-amd64")
	foundBinary := false
	for _, file := range zr.File {
		name := filepath.ToSlash(file.Name)
		if name == "sbp-panel-linux-amd64" {
			if err := extractZipFile(file, binaryPath, 0755); err != nil {
				return "", err
			}
			foundBinary = true
			continue
		}
	}
	if !foundBinary {
		return "", errors.New("the archive does not contain the Simple Bridge Panel binary")
	}
	return binaryPath, nil
}

func extractZipFile(file *zip.File, dst string, mode os.FileMode) error {
	if !file.Mode().IsRegular() {
		return errors.New("the archive contains an unsupported file type")
	}
	if file.UncompressedSize64 > 64<<20 {
		return errors.New("a file in the archive is too large")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	r, err := file.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(w, io.LimitReader(r, 64<<20+1))
	closeErr := w.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
