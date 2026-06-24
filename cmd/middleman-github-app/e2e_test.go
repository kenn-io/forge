package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/githubapp/githubapptest"
	"go.kenn.io/middleman/internal/tokenauth"
)

func writeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[repos]]
owner = "kenn-io"
name = "middleman"
`), 0o600))
	return path
}

// syncBuffer lets test goroutines watch CLI output while the command
// is still writing it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

func newTestEnv(t *testing.T, fake *githubapptest.Fake, configPath string) (*appEnv, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	env := &appEnv{
		stdout:       out,
		configPath:   configPath,
		apiBase:      fake.APIBase(),
		webBase:      fake.URL(),
		pollInterval: 10 * time.Millisecond,
		now:          time.Now,
		openBrowser: func(string) error {
			return fmt.Errorf("browser not scripted for this test")
		},
	}
	return env, out
}

var (
	installSlugRe  = regexp.MustCompile(`/apps/([^/]+)/installations/new`)
	settingsSlugRe = regexp.MustCompile(`/settings/apps/([^/]+)/advanced`)
)

// scriptBrowser plays the user: it submits the manifest form like a
// real browser would, clicks "install" by registering an installation
// on the fake, and confirms app deletion in fake settings.
func scriptBrowser(t *testing.T, fake *githubapptest.Fake, installAccount string) func(string) error {
	t.Helper()
	return scriptBrowserWithInstall(t, fake, func(appID int64) error {
		_, err := fake.Install(appID, installAccount)
		return err
	})
}

// scriptBrowserWithInstall is scriptBrowser with the install click
// replaced, so tests can simulate "Only select repositories" installs.
func scriptBrowserWithInstall(
	t *testing.T, fake *githubapptest.Fake, install func(appID int64) error,
) func(string) error {
	t.Helper()
	return func(target string) error {
		if m := installSlugRe.FindStringSubmatch(target); m != nil {
			app, ok := fake.AppBySlug(m[1])
			if !ok {
				return fmt.Errorf("install URL for unknown app slug %q", m[1])
			}
			return install(app.ID)
		}
		if m := settingsSlugRe.FindStringSubmatch(target); m != nil {
			app, ok := fake.AppBySlug(m[1])
			if !ok {
				return fmt.Errorf("settings URL for unknown app slug %q", m[1])
			}
			return fake.DeleteApp(app.ID)
		}
		return submitManifestForm(target)
	}
}

// submitManifestForm performs what the embedded Svelte setup page's
// JS does: read the flow contract from /flow.json and POST the
// manifest form to GitHub, following the redirect chain back through
// the CLI callback into the setup page's done view.
func submitManifestForm(pageURL string) error {
	resp, err := http.Get(strings.TrimRight(pageURL, "/") + "/flow.json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("flow.json returned %d", resp.StatusCode)
	}
	var flow flowJSON
	if err := json.NewDecoder(resp.Body).Decode(&flow); err != nil {
		return fmt.Errorf("decoding flow.json: %w", err)
	}
	if flow.Action == "" || flow.Manifest == "" {
		return fmt.Errorf("flow.json missing action or manifest: %+v", flow)
	}
	final, err := http.PostForm(flow.Action, url.Values{"manifest": {flow.Manifest}})
	if err != nil {
		return err
	}
	defer final.Body.Close()
	if final.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(final.Body)
		return fmt.Errorf("callback chain returned %d: %s", final.StatusCode, out)
	}
	// The callback must land the browser on the setup page's success
	// view, not a raw handler response.
	if got := final.Request.URL.Query().Get("step"); got != "done" {
		return fmt.Errorf("expected redirect to ?step=done, landed on %s", final.Request.URL)
	}
	return nil
}

// generateWrongKeyPEM returns a valid RSA private key PEM that does
// not belong to any app on the fake, simulating a rotated/stale key.
func generateWrongKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func createTestApp(t *testing.T, fake *githubapptest.Fake, configPath, name string) {
	t.Helper()
	env, _ := newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "kenn-io")
	require.NoError(t, runCLI([]string{
		"create", "--name", name, "--timeout", "10s",
	}, env))
}

func TestCreateFlowEndToEnd(t *testing.T) {
	t.Parallel()
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	env, out := newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "kenn-io")

	require := require.New(t)
	require.NoError(runCLI([]string{
		"create", "--name", "middleman-e2e", "--timeout", "10s",
	}, env))

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	app := cfg.GitHubApps[0]

	assert := assert.New(t)
	assert.Equal("github.com", app.Host)
	assert.Equal("middleman-e2e", app.Slug)
	assert.Positive(app.AppID)
	assert.NotZero(app.InstallationID)
	assert.Equal("kenn-io", app.InstallationAccount)
	assert.Contains(out.String(), "Installed on kenn-io")

	// The private key must exist next to the config, owner-only.
	info, err := os.Stat(app.PrivateKeyPath)
	require.NoError(err)
	assert.Equal(filepath.Dir(configPath), filepath.Dir(app.PrivateKeyPath))
	assert.Equal(os.FileMode(0o600), info.Mode().Perm())

	// The saved entry must put a mintable github_app candidate in the
	// host's token chain — that is the whole point of the tool.
	desc := cfg.TokenSourceForPlatformHost("github", "github.com", "", "")
	require.NotEmpty(desc.Candidates)
	first := desc.Candidates[0]
	assert.Equal(tokenauth.SourceKindGitHubApp, first.Kind)
	assert.Equal(app.AppID, first.AppID)
	assert.Equal(app.InstallationID, first.InstallationID)

	// The manifest GitHub received must keep webhooks off (middleman
	// polls) and stay private.
	manifests := fake.Manifests()
	require.Len(manifests, 1)
	var sent struct {
		Public         bool `json:"public"`
		HookAttributes struct {
			Active bool `json:"active"`
		} `json:"hook_attributes"`
		DefaultPermissions map[string]string `json:"default_permissions"`
	}
	require.NoError(json.Unmarshal([]byte(manifests[0]), &sent))
	assert.False(sent.Public)
	// The app stays read-only; mutations use the user's PAT chain.
	for scope, level := range sent.DefaultPermissions {
		assert.Equal("read", level, "permission %s", scope)
	}
	assert.False(sent.HookAttributes.Active)
	assert.Equal("read", sent.DefaultPermissions["contents"])
}

func TestCreateRefusesSecondAppForSameHost(t *testing.T) {
	t.Parallel()
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-first")

	env, _ := newTestEnv(t, fake, configPath)
	err := runCLI([]string{"create", "--name", "middleman-second"}, env)
	require.Error(t, err)
	assert.ErrorContains(t, err, "already exists")
}

func TestListReportsInstallationAndRateBudget(t *testing.T) {
	t.Parallel()
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-list")
	fake.SetRateRemaining(5000, 12)

	env, out := newTestEnv(t, fake, configPath)
	require.NoError(t, runCLI([]string{"list", "--json"}, env))

	var statuses []appStatus
	require.NoError(t, json.Unmarshal(out.Bytes(), &statuses))
	require.Len(t, statuses, 1)
	assert := assert.New(t)
	assert.Equal("middleman-list", statuses[0].Slug)
	assert.Equal("kenn-io", statuses[0].InstallationAccount)
	assert.Equal(5000, statuses[0].RateLimit)
	assert.Empty(statuses[0].Error)
	// Rate numbers come from a freshly minted installation token; the
	// fake mints with zero usage unless configured otherwise.
	assert.Equal(5000, statuses[0].RateRemaining)
}

func TestUninstallClearsInstallationButKeepsApp(t *testing.T) {
	t.Parallel()
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-uninst")

	require := require.New(t)
	env, _ := newTestEnv(t, fake, configPath)
	err := runCLI([]string{"uninstall"}, env)
	require.Error(err, "uninstall must demand --yes")
	require.ErrorContains(err, "--yes")

	env, _ = newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"uninstall", "--yes"}, env))

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	assert := assert.New(t)
	assert.Zero(cfg.GitHubApps[0].InstallationID)
	assert.Empty(cfg.GitHubApps[0].InstallationAccount)
	app, ok := fake.AppBySlug("middleman-uninst")
	require.True(ok)
	assert.Empty(app.Installations, "installation must be deleted on GitHub")
}

func TestInstallRecordsNewInstallation(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	// The repo carries its own token override, so installing on a
	// different account than the repo owner is a valid configuration.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(configPath, []byte(`
[[repos]]
owner = "kenn-io"
name = "middleman"
token_env = "KENN_IO_TOKEN"
`), 0o600))
	createTestApp(t, fake, configPath, "middleman-reinst")

	env, _ := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"uninstall", "--yes"}, env))

	env, _ = newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "other-org")
	require.NoError(runCLI([]string{"install", "--timeout", "10s"}, env))

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	assert.Equal(t, "other-org", cfg.GitHubApps[0].InstallationAccount)
	assert.NotZero(t, cfg.GitHubApps[0].InstallationID)
}

func TestInstallRejectsRecordedInstallWithoutRepositorySelection(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-no-selection")

	raw, err := os.ReadFile(configPath)
	require.NoError(err)
	broken := strings.Replace(string(raw), `repository_selection = "all"`+"\n", "", 1)
	require.NotEqual(string(raw), broken)
	require.NoError(os.WriteFile(configPath, []byte(broken), 0o600))

	env, _ := newTestEnv(t, fake, configPath)
	err = runCLI([]string{"install", "--timeout", "10s"}, env)
	require.Error(err)
	require.ErrorContains(err, "repository_selection is required")
}

func TestInstallHydratesMinimalAppMetadataBeforeOpeningInstallURL(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-minimal")

	env, _ := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"uninstall", "--yes"}, env))

	cfg, err := config.LoadForGitHubAppRepair(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	cfg.GitHubApps[0].Slug = ""
	cfg.GitHubApps[0].Owner = ""
	cfg.GitHubApps[0].OwnerType = ""
	require.NoError(cfg.Save(configPath))

	var opened string
	env, _ = newTestEnv(t, fake, configPath)
	env.openBrowser = func(target string) error {
		opened = target
		if !strings.Contains(target, "/apps/middleman-minimal/installations/new") {
			return fmt.Errorf("unexpected install URL: %s", target)
		}
		app, ok := fake.AppBySlug("middleman-minimal")
		if !ok {
			return fmt.Errorf("missing fake app middleman-minimal")
		}
		_, err := fake.Install(app.ID, "kenn-io")
		return err
	}
	require.NoError(runCLI([]string{"install", "--timeout", "10s"}, env))
	require.Contains(opened, "/apps/middleman-minimal/installations/new")

	cfg, err = config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	assert := assert.New(t)
	assert.Equal("middleman-minimal", cfg.GitHubApps[0].Slug)
	assert.Equal("fake-owner", cfg.GitHubApps[0].Owner)
	assert.Equal("User", cfg.GitHubApps[0].OwnerType)
	assert.Equal("kenn-io", cfg.GitHubApps[0].InstallationAccount)
	assert.NotZero(cfg.GitHubApps[0].InstallationID)
}

func TestInstallRefusesInstallationThatMissesConfiguredRepos(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-uncov")

	env, _ := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"uninstall", "--yes"}, env))

	// Installing on an account that does not own kenn-io/middleman
	// must not be recorded: the installation token cannot reach the
	// repo, so sync would 404 while the config looks healthy. The
	// flow reports the uncovering installation and keeps waiting for
	// one on the owning account instead of dead-ending.
	env, out := newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "someone-else")
	err := runCLI([]string{"install", "--timeout", "1s"}, env)
	require.Error(err)
	require.ErrorContains(err, "timed out")
	require.Contains(out.String(), "someone-else")
	require.Contains(out.String(), "kenn-io/middleman")
	require.Contains(out.String(), "Still waiting for an installation on the owning account")

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	assert.Zero(t, cfg.GitHubApps[0].InstallationID,
		"uncovered installation must not be recorded")
}

func TestDeleteRefusesWhenCredentialsCannotBeVerified(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-badcred")
	cfg, err := config.Load(configPath)
	require.NoError(err)
	keyPath := cfg.GitHubApps[0].PrivateKeyPath

	// A rotated/stale key makes GitHub reject the app JWT with 401.
	// Delete must not interpret that as "the app is gone" and wipe
	// local state while the app keeps its access on GitHub.
	wrongKey := generateWrongKeyPEM(t)
	require.NoError(os.WriteFile(keyPath, wrongKey, 0o600))

	env, _ := newTestEnv(t, fake, configPath)
	err = runCLI([]string{"delete", "--yes", "--timeout", "5s"}, env)
	require.Error(err)
	require.ErrorContains(err, "--local-only")

	cfg, err = config.Load(configPath)
	require.NoError(err)
	assert.Len(t, cfg.GitHubApps, 1, "config entry must survive unverified delete")
	assert.FileExists(t, keyPath)
}

func TestDeleteHydratesMinimalAppMetadataBeforeOpeningSettingsURL(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-delete-minimal")

	cfg, err := config.LoadForGitHubAppRepair(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	keyPath := cfg.GitHubApps[0].PrivateKeyPath
	cfg.GitHubApps[0].Slug = ""
	cfg.GitHubApps[0].Owner = ""
	cfg.GitHubApps[0].OwnerType = ""
	require.NoError(cfg.Save(configPath))

	var opened string
	env, _ := newTestEnv(t, fake, configPath)
	env.openBrowser = func(target string) error {
		opened = target
		if !strings.Contains(target, "/settings/apps/middleman-delete-minimal/advanced") {
			return fmt.Errorf("unexpected settings URL: %s", target)
		}
		app, ok := fake.AppBySlug("middleman-delete-minimal")
		if !ok {
			return fmt.Errorf("missing fake app middleman-delete-minimal")
		}
		return fake.DeleteApp(app.ID)
	}
	require.NoError(runCLI([]string{"delete", "--yes", "--timeout", "10s"}, env))
	require.Contains(opened, "/settings/apps/middleman-delete-minimal/advanced")

	cfg, err = config.LoadForGitHubAppRepair(configPath)
	require.NoError(err)
	assert := assert.New(t)
	assert.Empty(cfg.GitHubApps)
	assert.NoFileExists(keyPath)
}

func TestDeleteOpensSettingsForRepairInvalidConfig(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-delete-repair")
	cfg, err := config.LoadForGitHubAppRepair(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	keyPath := cfg.GitHubApps[0].PrivateKeyPath

	raw, err := os.ReadFile(configPath)
	require.NoError(err)
	broken := strings.Replace(string(raw), `installation_account = "kenn-io"`, `installation_account = "other-org"`, 1)
	require.NotEqual(string(raw), broken)
	require.NoError(os.WriteFile(configPath, []byte(broken), 0o600))
	_, err = config.Load(configPath)
	require.ErrorContains(err, "not covered by the github app")

	var opened string
	env, _ := newTestEnv(t, fake, configPath)
	env.openBrowser = func(target string) error {
		opened = target
		if !strings.Contains(target, "/settings/apps/middleman-delete-repair/advanced") {
			return fmt.Errorf("unexpected settings URL: %s", target)
		}
		app, ok := fake.AppBySlug("middleman-delete-repair")
		if !ok {
			return fmt.Errorf("missing fake app middleman-delete-repair")
		}
		return fake.DeleteApp(app.ID)
	}
	require.NoError(runCLI([]string{"delete", "--yes", "--timeout", "10s"}, env))
	require.Contains(opened, "/settings/apps/middleman-delete-repair/advanced")

	cfg, err = config.LoadForGitHubAppRepair(configPath)
	require.NoError(err)
	assert := assert.New(t)
	assert.Empty(cfg.GitHubApps)
	assert.NoFileExists(keyPath)
}

func TestInstallRejectsSelectedInstallMissingConfiguredRepos(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-sel")

	env, _ := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"uninstall", "--yes"}, env))

	// "Only select repositories" granting a different repo than the
	// configured kenn-io/middleman: the install must be refused and
	// stay unrecorded, because sync would 404 on the uncovered repo.
	env, _ = newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowserWithInstall(t, fake, func(appID int64) error {
		_, err := fake.InstallSelected(appID, "kenn-io", "kenn-io/other-repo")
		return err
	})
	err := runCLI([]string{"install", "--timeout", "10s"}, env)
	require.Error(err)
	require.ErrorContains(err, "Only select repositories")
	require.ErrorContains(err, "kenn-io/middleman")

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	assert.Zero(t, cfg.GitHubApps[0].InstallationID,
		"uncovered selected install must not be recorded")
}

func TestInstallAcceptsSelectedInstallCoveringConfiguredRepos(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-selok")

	env, _ := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"uninstall", "--yes"}, env))

	env, _ = newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowserWithInstall(t, fake, func(appID int64) error {
		_, err := fake.InstallSelected(appID, "kenn-io", "kenn-io/middleman")
		return err
	})
	require.NoError(runCLI([]string{"install", "--timeout", "10s"}, env))

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	assert := assert.New(t)
	assert.NotZero(cfg.GitHubApps[0].InstallationID)
	assert.Equal("kenn-io", cfg.GitHubApps[0].InstallationAccount)
	// The recorded selection lets config validation keep enforcing
	// coverage when repos are added later.
	assert.Equal("selected", cfg.GitHubApps[0].RepositorySelection)
	assert.Equal([]string{"kenn-io/middleman"}, cfg.GitHubApps[0].SelectedRepos)
}

func TestInstallRefreshesStaleSelectedRepoSnapshot(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-refresh")

	// Install with a selected-repository installation that can reach
	// both repos on GitHub.
	env, _ := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"uninstall", "--yes"}, env))
	env, _ = newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowserWithInstall(t, fake, func(appID int64) error {
		_, err := fake.InstallSelected(appID, "kenn-io", "kenn-io/middleman", "kenn-io/other")
		return err
	})
	require.NoError(runCLI([]string{"install", "--timeout", "10s"}, env))

	// Simulate the stale-snapshot failure mode the validation error
	// points users at: the config gains a repo that the recorded
	// snapshot does not list (here by shrinking the recorded list and
	// adding the repo), so a strict load now fails.
	raw, err := os.ReadFile(configPath)
	require.NoError(err)
	stale := strings.Replace(
		string(raw),
		`selected_repos = ["kenn-io/middleman", "kenn-io/other"]`,
		`selected_repos = ["kenn-io/middleman"]`,
		1,
	)
	stale = strings.Replace(stale, `[[repos]]`, `[[repos]]
owner = "kenn-io"
name = "other"

[[repos]]`, 1)
	require.NoError(os.WriteFile(configPath, []byte(stale), 0o600))
	_, err = config.Load(configPath)
	require.ErrorContains(err, "kenn-io/other is not in")

	// Re-running install must load the config anyway, find the
	// recorded installation, and refresh the snapshot — no browser
	// interaction needed.
	env, out := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"install", "--timeout", "10s"}, env))
	require.Contains(out.String(), "Refreshing recorded installation")

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	assert := assert.New(t)
	assert.Equal("selected", cfg.GitHubApps[0].RepositorySelection)
	assert.ElementsMatch(
		[]string{"kenn-io/middleman", "kenn-io/other"},
		cfg.GitHubApps[0].SelectedRepos,
	)
}

func TestInstallRepairsWrongAccountInstallation(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)

	// The app starts correctly installed for a config whose repos are
	// owned by wrongorg.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(configPath, []byte(`
[[repos]]
owner = "wrongorg"
name = "thing"
`), 0o600))
	env, _ := newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "wrongorg")
	require.NoError(runCLI([]string{
		"create", "--name", "middleman-wrong-account", "--timeout", "10s",
	}, env))

	// The user re-points middleman at repos the recorded installation's
	// account does not own; the strict loader now rejects the config.
	raw, err := os.ReadFile(configPath)
	require.NoError(err)
	repointed := strings.Replace(string(raw), `owner = "wrongorg"`, `owner = "kenn-io"`, 1)
	repointed = strings.Replace(repointed, `name = "thing"`, `name = "middleman"`, 1)
	require.NoError(os.WriteFile(configPath, []byte(repointed), 0o600))
	_, err = config.Load(configPath)
	require.ErrorContains(err, "not covered by the github app")

	// install must not dead-end on refreshing the wrong-account
	// installation: it falls through to waiting for an installation on
	// the owning account and records that one.
	env, out := newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "kenn-io")
	require.NoError(runCLI([]string{"install", "--timeout", "10s"}, env))
	require.Contains(out.String(), "waiting for an installation on the right account")

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	require.Equal("kenn-io", cfg.GitHubApps[0].InstallationAccount)
}

func TestInstallSkipsPreexistingUncoveringInstallation(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-preexist")

	env, _ := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"uninstall", "--yes"}, env))

	// An unrecorded installation on an account that does not own the
	// configured repos already exists before install runs. The poll
	// must not grab it as "the first new installation" and dead-end at
	// the coverage check; it ignores it and keeps waiting for the
	// installation the browser flow creates on the owning account.
	app, ok := fake.AppBySlug("middleman-preexist")
	require.True(ok)
	_, err := fake.Install(app.ID, "someone-else")
	require.NoError(err)

	env, out := newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "kenn-io")
	require.NoError(runCLI([]string{"install", "--timeout", "10s"}, env))
	require.Contains(out.String(), "Ignoring installation")
	require.Contains(out.String(), "someone-else")

	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	require.Equal("kenn-io", cfg.GitHubApps[0].InstallationAccount)
}

func TestCreateWithNestedRelativeConfigPath(t *testing.T) {
	// A relative --config with a directory component previously saved
	// a key path that later loads re-resolved against the config dir,
	// producing tmp/tmp/<key>.pem. The stored path must be absolute
	// and the key must load on a fresh config read.
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	dir := t.TempDir()
	t.Chdir(dir)
	require := require.New(t)
	require.NoError(os.MkdirAll("nested", 0o700))
	relConfig := filepath.Join("nested", "config.toml")
	require.NoError(os.WriteFile(relConfig, []byte(`
[[repos]]
owner = "kenn-io"
name = "middleman"
`), 0o600))

	env, _ := newTestEnv(t, fake, relConfig)
	env.openBrowser = scriptBrowser(t, fake, "kenn-io")
	require.NoError(runCLI([]string{
		"create", "--name", "middleman-relcfg", "--timeout", "10s",
	}, env))

	cfg, err := config.Load(relConfig)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	keyPath := cfg.GitHubApps[0].PrivateKeyPath
	assert := assert.New(t)
	assert.True(filepath.IsAbs(keyPath), "stored key path %q must be absolute", keyPath)
	assert.FileExists(keyPath)
	assert.Equal(filepath.Join(dir, "nested"), filepath.Dir(keyPath))
}

func TestListFlagsSelectedInstallMissingRepos(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-listsel")

	// Simulate a manually edited or restored config carrying a
	// selected-repository installation the CLI never verified.
	cfg, err := config.Load(configPath)
	require.NoError(err)
	app, ok := fake.AppBySlug("middleman-listsel")
	require.True(ok)
	installID, err := fake.InstallSelected(app.ID, "kenn-io", "kenn-io/other-repo")
	require.NoError(err)
	cfg.GitHubApps[0].InstallationID = installID
	require.NoError(cfg.Save(configPath))

	env, out := newTestEnv(t, fake, configPath)
	require.NoError(runCLI([]string{"list", "--json"}, env))
	var statuses []appStatus
	require.NoError(json.Unmarshal(out.Bytes(), &statuses))
	require.Len(statuses, 1)
	assert.Contains(t, statuses[0].Error, "kenn-io/middleman",
		"list must surface the uncovered configured repo")
}

func TestDeleteRemovesConfigAndKeyAfterBrowserDeletion(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-del")
	cfg, err := config.Load(configPath)
	require.NoError(err)
	keyPath := cfg.GitHubApps[0].PrivateKeyPath

	env, _ := newTestEnv(t, fake, configPath)
	err = runCLI([]string{"delete"}, env)
	require.Error(err, "delete must demand --yes")

	env, out := newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "kenn-io")
	require.NoError(runCLI([]string{"delete", "--yes", "--timeout", "10s"}, env))

	cfg, err = config.Load(configPath)
	require.NoError(err)
	assert := assert.New(t)
	assert.Empty(cfg.GitHubApps)
	assert.NoFileExists(keyPath)
	assert.Contains(out.String(), "Deleted app")
}

func TestDeletePreservesExternalPrivateKeyPath(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-byo-delete")
	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	generatedKeyPath := cfg.GitHubApps[0].PrivateKeyPath
	keyPEM, err := os.ReadFile(generatedKeyPath)
	require.NoError(err)
	externalKeyPath := filepath.Join(t.TempDir(), "shared-byo-app.pem")
	require.NoError(os.WriteFile(externalKeyPath, keyPEM, 0o600))
	cfg.GitHubApps[0].PrivateKeyPath = externalKeyPath
	require.NoError(cfg.Save(configPath))

	env, out := newTestEnv(t, fake, configPath)
	env.openBrowser = scriptBrowser(t, fake, "kenn-io")
	require.NoError(runCLI([]string{"delete", "--yes", "--timeout", "10s"}, env))

	cfg, err = config.Load(configPath)
	require.NoError(err)
	assert := assert.New(t)
	assert.Empty(cfg.GitHubApps)
	assert.FileExists(externalKeyPath)
	assert.Contains(out.String(), "Preserved external private key")
}

func TestDeleteRemovesGeneratedPrivateKeyAfterAppRename(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-rename-delete")
	cfg, err := config.Load(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	keyPath := cfg.GitHubApps[0].PrivateKeyPath
	require.Contains(filepath.Base(keyPath), "middleman-rename-delete")
	appID := cfg.GitHubApps[0].AppID
	require.NoError(fake.RenameApp(appID, "middleman-renamed-live"))

	var opened string
	env, _ := newTestEnv(t, fake, configPath)
	env.openBrowser = func(target string) error {
		opened = target
		if !strings.Contains(target, "/settings/apps/middleman-renamed-live/advanced") {
			return fmt.Errorf("unexpected settings URL: %s", target)
		}
		app, ok := fake.AppBySlug("middleman-renamed-live")
		if !ok {
			return fmt.Errorf("missing fake app middleman-renamed-live")
		}
		return fake.DeleteApp(app.ID)
	}
	require.NoError(runCLI([]string{"delete", "--yes", "--timeout", "10s"}, env))
	require.Contains(opened, "/settings/apps/middleman-renamed-live/advanced")

	cfg, err = config.LoadForGitHubAppRepair(configPath)
	require.NoError(err)
	assert := assert.New(t)
	assert.Empty(cfg.GitHubApps)
	assert.NoFileExists(keyPath)
}

func TestOpenHydratesMinimalAppMetadataBeforeOpeningSettingsURL(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-open-minimal")

	cfg, err := config.LoadForGitHubAppRepair(configPath)
	require.NoError(err)
	require.Len(cfg.GitHubApps, 1)
	cfg.GitHubApps[0].Slug = ""
	cfg.GitHubApps[0].Owner = ""
	cfg.GitHubApps[0].OwnerType = ""
	require.NoError(cfg.Save(configPath))

	var opened string
	env, _ := newTestEnv(t, fake, configPath)
	env.openBrowser = func(target string) error {
		opened = target
		if !strings.Contains(target, "/settings/apps/middleman-open-minimal") {
			return fmt.Errorf("unexpected settings URL: %s", target)
		}
		return nil
	}
	require.NoError(runCLI([]string{"open"}, env))
	require.Contains(opened, "/settings/apps/middleman-open-minimal")
}

func TestOpenOpensSettingsForRepairInvalidConfig(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	createTestApp(t, fake, configPath, "middleman-open-repair")

	raw, err := os.ReadFile(configPath)
	require.NoError(err)
	broken := strings.Replace(string(raw), `installation_account = "kenn-io"`, `installation_account = "other-org"`, 1)
	require.NotEqual(string(raw), broken)
	require.NoError(os.WriteFile(configPath, []byte(broken), 0o600))
	_, err = config.Load(configPath)
	require.ErrorContains(err, "not covered by the github app")

	var opened string
	env, _ := newTestEnv(t, fake, configPath)
	env.openBrowser = func(target string) error {
		opened = target
		if !strings.Contains(target, "/settings/apps/middleman-open-repair") {
			return fmt.Errorf("unexpected settings URL: %s", target)
		}
		return nil
	}
	require.NoError(runCLI([]string{"open"}, env))
	require.Contains(opened, "/settings/apps/middleman-open-repair")
}

func TestCreateNoBrowserPrintsManifestURL(t *testing.T) {
	t.Parallel()
	fake := githubapptest.NewFake()
	t.Cleanup(fake.Close)
	configPath := writeTestConfig(t)
	env, out := newTestEnv(t, fake, configPath)
	// No browser scripted: drive the flow from the printed URL like a
	// user pasting it into a browser by hand.
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if m := regexp.MustCompile(`http://127\.0\.0\.1:\d+/\S+`).FindString(out.String()); m != "" {
				u, err := url.Parse(m)
				if err != nil {
					done <- err
					return
				}
				if u.Path == "/" {
					done <- fmt.Errorf("manifest setup URL must include an unguessable path: %s", m)
					return
				}
				done <- submitManifestForm(m)
				return
			}
			if time.Now().After(deadline) {
				done <- fmt.Errorf("manifest URL never printed; output: %s", out.String())
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	go func() {
		// Approve the install once the app exists.
		deadline := time.Now().Add(5 * time.Second)
		for {
			if app, ok := fake.AppBySlug("middleman-nobrowser"); ok {
				_, _ = fake.Install(app.ID, "kenn-io")
				return
			}
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	require.NoError(t, runCLI([]string{
		"create", "--name", "middleman-nobrowser", "--no-browser", "--timeout", "10s",
	}, env))
	require.NoError(t, <-done)
	assert.Contains(t, out.String(), "Installed on kenn-io", out.String())
}
