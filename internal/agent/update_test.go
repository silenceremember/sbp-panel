package agent

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/silenceremember/sbp-panel/internal/buildinfo"
	"github.com/silenceremember/sbp-panel/internal/config"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.7.0", "0.6.0", 1},
		{"1.0.0", "1.0.0", 0},
		{"1.2.3", "2.0.0", -1},
		{"1.10.0", "1.9.9", 1},
	}
	for _, test := range tests {
		if got := compareVersions(test.a, test.b); got != test.want {
			t.Fatalf("compareVersions(%q, %q)=%d, want %d", test.a, test.b, got, test.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	if _, err := parseVersion(buildinfo.Version); err != nil {
		t.Fatalf("build version %q is invalid: %v", buildinfo.Version, err)
	}
	valid := []struct {
		value string
		want  parsedVersion
	}{
		{"1.2.3", parsedVersion{1, 2, 3}},
		{"v1.0.0", parsedVersion{1, 0, 0}},
	}
	for _, test := range valid {
		got, err := parseVersion(test.value)
		if err != nil || got != test.want {
			t.Fatalf("parseVersion(%q)=(%#v, %v), want %#v", test.value, got, err, test.want)
		}
	}
	for _, value := range []string{"1.0", "1.0.0-pre.1", "1.0.0-rc.1", "1.0.0+build"} {
		if _, err := parseVersion(value); err == nil {
			t.Fatalf("parseVersion(%q) unexpectedly succeeded", value)
		}
	}
}

func TestCheckUpdateUsesReleaseMetadataWithoutGitHubAPI(t *testing.T) {
	metadata := updateMetadata{
		Version:   "99.0.0",
		AssetName: "sbp-panel-99.0.0-linux-amd64.zip",
		SHA256:    string(bytes.Repeat([]byte{'a'}, 64)),
		Size:      1024,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sbp-panel-update.json" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()

	info, err := checkUpdateAt(server.Client(), server.URL+"/sbp-panel-update.json", false)
	if err != nil {
		t.Fatal(err)
	}
	if !info.UpdateAvailable || info.LatestVersion != metadata.Version || info.AssetName != metadata.AssetName {
		t.Fatalf("unexpected update info: %#v", info)
	}
	if info.asset.Digest != "sha256:"+metadata.SHA256 {
		t.Fatalf("unexpected digest: %q", info.asset.Digest)
	}
}

func TestIncludePrereleasesFromQuery(t *testing.T) {
	for _, test := range []struct {
		query string
		want  bool
		valid bool
	}{
		{"", false, true},
		{"include_prereleases=1", true, true},
		{"include_prereleases=0", false, false},
		{"include_prereleases=1&extra=1", false, false},
		{"include_prereleases=%31", false, false},
	} {
		got, err := includePrereleasesFromQuery(test.query)
		if got != test.want || (err == nil) != test.valid {
			t.Fatalf("includePrereleasesFromQuery(%q)=(%v, %v)", test.query, got, err)
		}
	}
}

func TestCheckUpdateChannels(t *testing.T) {
	apiCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stable.json":
			_ = json.NewEncoder(w).Encode(updateMetadata{
				Version: "60.0.0", AssetName: "sbp-panel-60.0.0-linux-amd64.zip",
				SHA256: strings.Repeat("a", 64), Size: 1024,
			})
		case "/releases":
			apiCalls++
			if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" || r.Header.Get("User-Agent") == "" {
				t.Fatalf("missing GitHub API headers: %#v", r.Header)
			}
			_, _ = io.WriteString(w, `[
				{"tag_name":"v99.0.0","draft":true,"prerelease":true,"assets":[{"name":"sbp-panel-update.json"}]},
				{"tag_name":"v90.0.0","prerelease":true,"assets":[]},
				{"tag_name":"v80.0.0-pre.1","prerelease":true,"assets":[{"name":"sbp-panel-update.json"}]},
				{"tag_name":"70.0.0","prerelease":true,"assets":[{"name":"sbp-panel-update.json"}]},
				{"tag_name":"v70.0.0","prerelease":true,"assets":[{"name":"sbp-panel-update.json"}]},
				{"tag_name":"v65.0.0","assets":[{"name":"sbp-panel-update.json"},{"name":"sbp-panel-update.json"}]},
				{"tag_name":"v60.0.0","assets":[{"name":"sbp-panel-update.json"}]}
			]`)
		case "/download/v70.0.0/sbp-panel-update.json":
			_ = json.NewEncoder(w).Encode(updateMetadata{
				Version: "70.0.0", AssetName: "sbp-panel-70.0.0-linux-amd64.zip",
				SHA256: strings.Repeat("b", 64), Size: 2048,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stable, err := checkUpdateWithURLs(server.Client(), false, server.URL+"/stable.json", server.URL+"/releases", server.URL+"/download")
	if err != nil || stable.LatestVersion != "60.0.0" || stable.IncludePrereleases || apiCalls != 0 {
		t.Fatalf("stable channel = %#v, calls=%d, err=%v", stable, apiCalls, err)
	}
	all, err := checkUpdateWithURLs(server.Client(), true, server.URL+"/stable.json", server.URL+"/releases", server.URL+"/download")
	if err != nil || all.LatestVersion != "70.0.0" || !all.IncludePrereleases || apiCalls != 1 {
		t.Fatalf("all-release channel = %#v, calls=%d, err=%v", all, apiCalls, err)
	}
}

func TestLatestReleaseMetadataURLBoundsReleaseCount(t *testing.T) {
	releases := make([]map[string]any, maxReleaseCount+1)
	for i := range releases {
		releases[i] = map[string]any{"tag_name": "v1.0.0", "assets": []map[string]string{{"name": "sbp-panel-update.json"}}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(releases)
	}))
	defer server.Close()
	if _, _, err := latestReleaseMetadataURL(server.Client(), server.URL, server.URL); err == nil || !strings.Contains(err.Error(), "too many releases") {
		t.Fatalf("release count was not rejected: %v", err)
	}
}

func TestAllReleaseChannelRejectsMetadataFromAnotherTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases" {
			_, _ = io.WriteString(w, `[{"tag_name":"v2.0.0","prerelease":true,"assets":[{"name":"sbp-panel-update.json"}]}]`)
			return
		}
		_ = json.NewEncoder(w).Encode(updateMetadata{
			Version: "3.0.0", AssetName: "sbp-panel-3.0.0-linux-amd64.zip",
			SHA256: strings.Repeat("c", 64), Size: 1024,
		})
	}))
	defer server.Close()
	_, err := checkUpdateWithURLs(server.Client(), true, server.URL+"/stable.json", server.URL+"/releases", server.URL+"/download")
	if err == nil || !strings.Contains(err.Error(), "does not match tag") {
		t.Fatalf("mismatched metadata was not rejected: %v", err)
	}
}

func TestExtractUpdateArchive(t *testing.T) {
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	for name, content := range map[string]string{
		"sbp-panel-linux-amd64": "binary",
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	_ = zw.Close()
	root := t.TempDir()
	binary, err := extractUpdateArchive(body.Bytes(), root)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(binary); string(b) != "binary" {
		t.Fatalf("wrong binary: %q", b)
	}
}

func TestCleanupUpdateStagingRemovesOnlyOwnedDirectories(t *testing.T) {
	original := updateStagingRoot
	updateStagingRoot = t.TempDir()
	t.Cleanup(func() { updateStagingRoot = original })

	for _, name := range []string{".update-12345", ".update-manual", "update-12345"} {
		if err := os.Mkdir(filepath.Join(updateStagingRoot, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(updateStagingRoot, ".update-67890"), []byte("not a staging directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupUpdateStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(updateStagingRoot, ".update-12345")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned staging directory remained: %v", err)
	}
	for _, name := range []string{".update-manual", "update-12345", ".update-67890"} {
		if _, err := os.Stat(filepath.Join(updateStagingRoot, name)); err != nil {
			t.Fatalf("unrelated path %q was removed: %v", name, err)
		}
	}
}

func TestUpdateProgressSurvivesServiceRestart(t *testing.T) {
	original, originalTransaction := updateProgressPath, updateTransactionPath
	updateProgressPath = filepath.Join(t.TempDir(), "update-progress.json")
	updateTransactionPath = filepath.Join(t.TempDir(), "update-transaction.json")
	t.Cleanup(func() {
		updateProgressPath = original
		updateTransactionPath = originalTransaction
	})

	writeUpdateProgress(UpdateProgress{Status: "restarting", Progress: 96, Stage: "Restarting services.", Target: buildinfo.Version})
	reconcileUpdateProgress()
	progress := readUpdateProgress()
	if progress.Status != "done" || progress.Progress != 100 || progress.Target != buildinfo.Version {
		t.Fatalf("unexpected reconciled progress: %#v", progress)
	}
}

func TestUpdateWatchdogCommitsOnlyAfterHealthConfirmation(t *testing.T) {
	_, transaction, configPath := prepareWatchdogTest(t)
	checks := 0
	updateHealthProbe = func(config.Config) error {
		checks++
		for _, path := range []string{updateTransactionPath, transaction.BinaryRollbackPath} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("rollback artifact disappeared before health confirmation: %s: %v", path, err)
			}
		}
		return nil
	}
	restarts := 0
	updateCommand = func(name string, args ...string) (string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
			restarts++
		}
		return "", nil
	}

	if err := RunUpdateWatchdog(configPath); err != nil {
		t.Fatal(err)
	}
	if checks != 1 || restarts != 1 {
		t.Fatalf("unexpected watchdog activity: checks=%d restarts=%d", checks, restarts)
	}
	for _, path := range []string{updateTransactionPath, transaction.BinaryRollbackPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("committed update left %s behind: %v", path, err)
		}
	}
	if body, _ := os.ReadFile(transaction.BinaryPath); string(body) != "new binary" {
		t.Fatalf("commit replaced the new binary: %q", body)
	}
	if progress := readUpdateProgress(); progress.Status != "done" || progress.Progress != 100 {
		t.Fatalf("unexpected committed progress: %#v", progress)
	}
}

func TestUpdateWatchdogRollsBackAfterHealthTimeout(t *testing.T) {
	_, transaction, configPath := prepareWatchdogTest(t)
	restarts := 0
	updateCommand = func(name string, args ...string) (string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
			restarts++
		}
		return "", nil
	}
	updateHealthProbe = func(config.Config) error {
		if restarts < 2 {
			return errors.New("new services are unhealthy")
		}
		return nil
	}
	updateHealthTimeout = 3 * time.Millisecond
	updateHealthInterval = time.Millisecond

	if err := RunUpdateWatchdog(configPath); err != nil {
		t.Fatal(err)
	}
	if restarts != 2 {
		t.Fatalf("expected new start and rollback start, got %d restarts", restarts)
	}
	if body, _ := os.ReadFile(transaction.BinaryPath); string(body) != "old binary" {
		t.Fatalf("binary was not rolled back: %q", body)
	}
	if _, err := os.Stat(updateTransactionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful rollback left a transaction behind: %v", err)
	}
	progress := readUpdateProgress()
	if progress.Status != "error" || progress.Stage != "Update rolled back safely." || !strings.Contains(progress.Error, "unhealthy") {
		t.Fatalf("unexpected rollback progress: %#v", progress)
	}
}

func TestUpdateWatchdogPreservesRecoveryStateWhenRollbackFails(t *testing.T) {
	_, transaction, configPath := prepareWatchdogTest(t)
	if err := os.Remove(transaction.BinaryRollbackPath); err != nil {
		t.Fatal(err)
	}
	updateCommand = func(string, ...string) (string, error) { return "", nil }
	updateHealthProbe = func(config.Config) error { return errors.New("unhealthy") }
	updateHealthTimeout = time.Millisecond
	updateHealthInterval = time.Millisecond

	if err := RunUpdateWatchdog(configPath); err == nil {
		t.Fatal("watchdog accepted a rollback without the preserved binary")
	}
	if _, err := os.Stat(updateTransactionPath); err != nil {
		t.Fatalf("failed rollback discarded its transaction: %v", err)
	}
	if progress := readUpdateProgress(); progress.Status != "error" || !strings.Contains(progress.Error, "preserved binary is missing") {
		t.Fatalf("unexpected failed rollback progress: %#v", progress)
	}
}

func TestReconcileKeepsRollbackArtifactsUntilWatchdogFinishes(t *testing.T) {
	_, transaction, _ := prepareWatchdogTest(t)
	scheduled := 0
	updateWatchdogScheduler = func(path string) error {
		scheduled++
		if path != transaction.ConfigPath {
			t.Fatalf("unexpected watchdog config path: %q", path)
		}
		return nil
	}
	writeUpdateProgress(UpdateProgress{Status: "restarting", Progress: 96, Stage: "Restarting services.", Target: transaction.Target})

	reconcileUpdateProgress()
	if scheduled != 1 {
		t.Fatalf("watchdog was scheduled %d times", scheduled)
	}
	if progress := readUpdateProgress(); progress.Status != "restarting" || progress.Progress != 97 {
		t.Fatalf("reconcile committed prematurely: %#v", progress)
	}
	for _, path := range []string{updateTransactionPath, transaction.BinaryRollbackPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("reconcile removed rollback artifact %s: %v", path, err)
		}
	}
}

func TestLocalPanelURLUsesLoopbackForWildcardListeners(t *testing.T) {
	for listen, want := range map[string]string{
		":9443":        "https://127.0.0.1:9443",
		"0.0.0.0:8443": "https://127.0.0.1:8443",
		"[::]:7443":    "https://[::1]:7443",
	} {
		if got := localPanelURL(listen); got != want {
			t.Fatalf("localPanelURL(%q)=%q, want %q", listen, got, want)
		}
	}
}

func prepareWatchdogTest(t *testing.T) (config.Config, updateTransaction, string) {
	t.Helper()
	originalProgress := updateProgressPath
	originalTransaction := updateTransactionPath
	originalBinary := updateBinaryPath
	originalTimeout := updateHealthTimeout
	originalInterval := updateHealthInterval
	originalChecks := updateRequiredHealthyChecks
	originalCommand := updateCommand
	originalProbe := updateHealthProbe
	originalScheduler := updateWatchdogScheduler
	t.Cleanup(func() {
		updateProgressPath = originalProgress
		updateTransactionPath = originalTransaction
		updateBinaryPath = originalBinary
		updateHealthTimeout = originalTimeout
		updateHealthInterval = originalInterval
		updateRequiredHealthyChecks = originalChecks
		updateCommand = originalCommand
		updateHealthProbe = originalProbe
		updateWatchdogScheduler = originalScheduler
	})

	root := t.TempDir()
	updateProgressPath = filepath.Join(root, "run", "update-progress.json")
	updateTransactionPath = filepath.Join(root, "state", "update-transaction.json")
	updateBinaryPath = filepath.Join(root, "bin", "vpn-panel")
	updateHealthTimeout = 20 * time.Millisecond
	updateHealthInterval = time.Millisecond
	updateRequiredHealthyChecks = 1
	if err := os.MkdirAll(filepath.Dir(updateBinaryPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updateBinaryPath, []byte("new binary"), 0755); err != nil {
		t.Fatal(err)
	}
	binaryRollback := updateBinaryPath + ".update-rollback"
	if err := os.WriteFile(binaryRollback, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	previousHash, _ := fileSHA256(binaryRollback)
	newHash, _ := fileSHA256(updateBinaryPath)
	configPath := filepath.Join(root, "config.json")
	c := config.Config{Listen: ":9443", AgentSocket: filepath.Join(root, "agent.sock")}
	configBody, _ := json.Marshal(c)
	if err := os.WriteFile(configPath, configBody, 0600); err != nil {
		t.Fatal(err)
	}
	transaction := updateTransaction{
		Target:               buildinfo.Version,
		Phase:                "installed",
		ConfigPath:           configPath,
		BinaryPath:           updateBinaryPath,
		BinaryRollbackPath:   binaryRollback,
		PreviousBinarySHA256: previousHash,
		NewBinarySHA256:      newHash,
	}
	if err := writeUpdateTransaction(transaction); err != nil {
		t.Fatal(err)
	}
	return c, transaction, configPath
}

func TestReadUpdateArchiveReportsDownloadProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 256<<10)
	var reported []int
	body, err := readUpdateArchive(bytes.NewReader(payload), int64(len(payload)), func(progress int) {
		reported = append(reported, progress)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, payload) {
		t.Fatal("downloaded archive does not match the payload")
	}
	if len(reported) == 0 || reported[len(reported)-1] != 59 {
		t.Fatalf("unexpected progress reports: %#v", reported)
	}
}

func TestReadUpdateArchiveRejectsMetadataSizeMismatch(t *testing.T) {
	if _, err := readUpdateArchive(strings.NewReader("short"), 100, nil); err == nil {
		t.Fatal("accepted an archive shorter than release metadata")
	}
}
