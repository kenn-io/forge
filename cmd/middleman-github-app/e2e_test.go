package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	formActionRe   = regexp.MustCompile(`action="([^"]+)"`)
	manifestValRe  = regexp.MustCompile(`name="manifest" value="([^"]+)"`)
	installSlugRe  = regexp.MustCompile(`/apps/([^/]+)/installations/new`)
	settingsSlugRe = regexp.MustCompile(`/settings/apps/([^/]+)/advanced`)
)

// scriptBrowser plays the user: it submits the manifest form like a
// real browser would, clicks "install" by registering an installation
// on the fake, and confirms app deletion in fake settings.
func scriptBrowser(t *testing.T, fake *githubapptest.Fake, installAccount string) func(string) error {
	t.Helper()
	return func(target string) error {
		go func() {
			if m := installSlugRe.FindStringSubmatch(target); m != nil {
				app, ok := fake.AppBySlug(m[1])
				if !assert.True(t, ok, "install URL for unknown app slug %q", m[1]) {
					return
				}
				_, err := fake.Install(app.ID, installAccount)
				assert.NoError(t, err)
				return
			}
			if m := settingsSlugRe.FindStringSubmatch(target); m != nil {
				app, ok := fake.AppBySlug(m[1])
				if !assert.True(t, ok, "settings URL for unknown app slug %q", m[1]) {
					return
				}
				assert.NoError(t, fake.DeleteApp(app.ID))
				return
			}
			assert.NoError(t, submitManifestForm(target))
		}()
		return nil
	}
}

// submitManifestForm fetches the CLI's loopback page and performs the
// form POST a browser would run, following GitHub's redirect back to
// the CLI callback.
func submitManifestForm(pageURL string) error {
	resp, err := http.Get(pageURL)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	action := formActionRe.FindSubmatch(body)
	manifest := manifestValRe.FindSubmatch(body)
	if action == nil || manifest == nil {
		return fmt.Errorf("manifest page did not contain a form: %s", body)
	}
	final, err := http.PostForm(
		html.UnescapeString(string(action[1])),
		url.Values{"manifest": {html.UnescapeString(string(manifest[1]))}},
	)
	if err != nil {
		return err
	}
	defer final.Body.Close()
	if final.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(final.Body)
		return fmt.Errorf("callback returned %d: %s", final.StatusCode, out)
	}
	return nil
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
	assert.False(sent.HookAttributes.Active)
	assert.Equal("write", sent.DefaultPermissions["contents"])
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
	configPath := writeTestConfig(t)
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
			if m := regexp.MustCompile(`http://127\.0\.0\.1:\d+`).FindString(out.String()); m != "" {
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
