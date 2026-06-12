package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/githubapp"
	"go.kenn.io/middleman/internal/githubapp/ui"
)

func runCreate(args []string, env *appEnv) error {
	fs := flag.NewFlagSet("middleman-github-app create", flag.ContinueOnError)
	fs.SetOutput(env.stdout)
	configPath := fs.String("config", env.configPath, "middleman config path")
	host := fs.String("host", "", "GitHub host (default github.com)")
	org := fs.String("org", "", "create the app under this organization instead of your user")
	name := fs.String("name", "", "app name (default middleman-<random>)")
	homepage := fs.String("homepage", "", "app homepage URL shown on its public page")
	noBrowser := fs.Bool("no-browser", false, "print URLs instead of opening a browser")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait for each browser step")
	registerTestFlags(fs, env)
	if err := fs.Parse(args); err != nil {
		return err
	}
	env.configPath = *configPath
	h := normalizeHostFlag(*host)

	if err := config.EnsureDefault(env.configPath); err != nil {
		return fmt.Errorf("ensuring middleman config exists: %w", err)
	}
	cfg, err := env.loadConfig()
	if err != nil {
		return err
	}
	if existing, ok := cfg.GitHubAppForHost(h); ok {
		return fmt.Errorf(
			"a github app for host %q already exists (app id %d, slug %q); "+
				"use \"install\" to add an installation or \"delete\" to replace it",
			h, existing.AppID, existing.Slug,
		)
	}

	appName := strings.TrimSpace(*name)
	if appName == "" {
		appName, err = githubapp.RandomAppName()
		if err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// The flow server stays up through the install step so the browser
	// can finish loading the setup page's "done" view after GitHub
	// redirects back.
	flow, err := newFlowServer(env.stdout)
	if err != nil {
		return err
	}
	defer flow.Close()

	creds, err := env.runManifestFlow(ctx, flow, manifestFlowOptions{
		host:     h,
		org:      *org,
		name:     appName,
		homepage: *homepage,
		open:     !*noBrowser,
		timeout:  *timeout,
	})
	if err != nil {
		return err
	}

	keyPath, err := writePrivateKey(env.configPath, h, creds.Slug, creds.PEM)
	if err != nil {
		return err
	}
	app := config.GitHubAppConfig{
		Host:           h,
		AppID:          creds.ID,
		Slug:           creds.Slug,
		Owner:          creds.Owner.Login,
		OwnerType:      creds.Owner.Type,
		PrivateKeyPath: keyPath,
	}
	if err := updateAppInConfig(cfg, env.configPath, app); err != nil {
		return fmt.Errorf("saving app credentials to config: %w", err)
	}
	fmt.Fprintf(env.stdout,
		"Created GitHub App %q (id %d) owned by %s.\n"+
			"Private key: %s\nConfig updated: %s\n",
		creds.Slug, creds.ID, creds.Owner.Login, keyPath, env.configPath,
	)

	// Step two: the app must be installed on the account that owns the
	// synced repos before it can mint tokens.
	if err := env.runInstallFlow(ctx, cfg, app, !*noBrowser, *timeout); err != nil {
		return fmt.Errorf(
			"app created but not installed yet: %w\n"+
				"run \"middleman-github-app install\" to finish", err,
		)
	}
	return nil
}

type manifestFlowOptions struct {
	host     string
	org      string
	name     string
	homepage string
	open     bool
	timeout  time.Duration
}

// flowServer is the loopback HTTP server backing the browser side of
// app creation. It serves the embedded Svelte setup page, exposes the
// manifest hand-off contract at /flow.json, and receives GitHub's
// post-creation redirect. It outlives the manifest exchange so the
// browser can still load the page's assets for the "done" view while
// the terminal continues with the install step.
type flowServer struct {
	localBase    string
	callbackPath string
	state        string
	listener     net.Listener
	srv          *http.Server
	codeCh       chan string
	errCh        chan error

	mu       sync.Mutex
	manifest string
	action   string
	appName  string
	host     string
	consumed bool
}

// missingUIPage is served when the binary was built without the
// embedded Svelte setup page (plain `go build`). The flow cannot
// continue in the browser, so say that instead of dead-ending.
const missingUIPage = `<!DOCTYPE html><html><head><title>middleman-github-app</title></head><body>
<h1>Setup page not included in this build</h1>
<p>This middleman-github-app binary was built without the embedded setup UI,
so the GitHub App creation flow cannot continue in the browser.</p>
<p>Rebuild with <code>make build</code> (which builds and embeds the page),
then re-run <code>middleman-github-app create</code>.</p>
</body></html>`

// flowJSON is the contract between the Go flow server and the Svelte
// setup page: the page POSTs manifest to action exactly as a plain
// HTML form would.
type flowJSON struct {
	Action   string `json:"action"`
	Manifest string `json:"manifest"`
	Name     string `json:"name"`
	Host     string `json:"host"`
}

func newFlowServer(stdout io.Writer) (*flowServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting local callback server: %w", err)
	}
	state, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	assets, err := ui.Assets()
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("loading embedded setup page: %w", err)
	}
	hasBuiltApp := ui.HasBuiltApp()
	if !hasBuiltApp {
		fmt.Fprintln(stdout,
			"warning: this binary was built without the setup page (plain go build); "+
				"the browser shows instructions instead. Build with `make build`.")
	}

	fs := &flowServer{
		localBase: "http://" + listener.Addr().String(),
		// The callback path is itself unguessable, so a forged request
		// cannot hit the handler even if the state echo were dropped.
		callbackPath: "/callback/" + state,
		state:        state,
		listener:     listener,
		codeCh:       make(chan string, 1),
		errCh:        make(chan error, 1),
	}

	mux := http.NewServeMux()
	if hasBuiltApp {
		mux.Handle("GET /", http.FileServerFS(assets))
	} else {
		// Without the embedded page the flow is a dead end in the
		// browser; explain that instead of serving a stub directory.
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, missingUIPage)
		})
	}
	mux.HandleFunc("GET /flow.json", fs.handleFlowJSON)
	mux.HandleFunc("GET "+fs.callbackPath, fs.handleCallback)
	fs.srv = &http.Server{Handler: mux}
	go func() {
		if err := fs.srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case fs.errCh <- err:
			default:
			}
		}
	}()
	return fs, nil
}

func (f *flowServer) Close() {
	_ = f.srv.Close()
	_ = f.listener.Close()
}

func (f *flowServer) setFlow(action, manifest, appName, host string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.action = action
	f.manifest = manifest
	f.appName = appName
	f.host = host
}

func (f *flowServer) handleFlowJSON(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	flow := flowJSON{
		Action:   f.action,
		Manifest: f.manifest,
		Name:     f.appName,
		Host:     f.host,
	}
	consumed := f.consumed
	f.mu.Unlock()
	// Once GitHub has redirected back, re-serving the manifest would
	// let a refreshed create tab auto-submit again and register a
	// second app that nothing records. The done view's assets keep
	// being served; only the hand-off contract dies.
	if consumed || flow.Action == "" {
		http.Error(w, "no app creation in progress", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(flow)
}

func (f *flowServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	gotState := r.URL.Query().Get("state")
	if gotState != "" &&
		subtle.ConstantTimeCompare([]byte(gotState), []byte(f.state)) != 1 {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.consumed = true
	f.mu.Unlock()
	select {
	case f.codeCh <- code:
	default:
	}
	// Hand the browser to the setup page's success view; the code
	// exchange continues in the terminal.
	http.Redirect(w, r, "/?step=done", http.StatusFound)
}

// runManifestFlow points the user's browser at the setup page, which
// submits the prepared manifest to GitHub, then exchanges the code
// GitHub redirects back with for the new app's credentials.
func (env *appEnv) runManifestFlow(
	ctx context.Context, flow *flowServer, opts manifestFlowOptions,
) (*githubapp.AppCredentials, error) {
	manifest, err := githubapp.NewManifest(
		opts.name, opts.homepage, flow.localBase+flow.callbackPath,
	)
	if err != nil {
		return nil, err
	}
	manifestJSON, err := manifest.JSON()
	if err != nil {
		return nil, err
	}
	action := env.webBaseFor(opts.host) + "/settings/apps/new?state=" + flow.state
	if opts.org != "" {
		action = env.webBaseFor(opts.host) +
			"/organizations/" + opts.org + "/settings/apps/new?state=" + flow.state
	}
	flow.setFlow(action, manifestJSON, opts.name, opts.host)

	fmt.Fprintf(env.stdout,
		"Open this page to create the app (it hands a prepared manifest to GitHub):\n  %s\n",
		flow.localBase,
	)
	if opts.open {
		if err := env.openBrowser(flow.localBase); err != nil {
			fmt.Fprintf(env.stdout, "could not open browser: %v\n", err)
		}
	}
	fmt.Fprintln(env.stdout, "Waiting for GitHub to redirect back after you click Create...")

	var code string
	select {
	case code = <-flow.codeCh:
	case err := <-flow.errCh:
		return nil, fmt.Errorf("local callback server failed: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(opts.timeout):
		return nil, fmt.Errorf("timed out after %s waiting for app creation", opts.timeout)
	}

	creds, err := env.apiClient(opts.host).ConvertManifest(ctx, code)
	if err != nil {
		return nil, err
	}
	return creds, nil
}

// runInstallFlow opens the app's install page and waits until an
// installation appears, then records it in the config.
func (env *appEnv) runInstallFlow(
	ctx context.Context,
	cfg *config.Config,
	app config.GitHubAppConfig,
	open bool,
	timeout time.Duration,
) error {
	url := installURL(env.webBaseFor(app.Host), app)
	fmt.Fprintf(env.stdout,
		"Install the app on the account that owns your synced repos:\n  %s\n", url,
	)
	if open {
		if err := env.openBrowser(url); err != nil {
			fmt.Fprintf(env.stdout, "could not open browser: %v\n", err)
		}
	}
	fmt.Fprintln(env.stdout, "Waiting for the installation to appear...")

	client := env.apiClient(app.Host)
	known := make(map[int64]struct{})
	if app.InstallationID != 0 {
		known[app.InstallationID] = struct{}{}
	}
	var picked githubapp.Installation
	err := env.pollUntil(ctx, timeout, func(ctx context.Context) (bool, error) {
		jwt, err := appJWT(app, env.now())
		if err != nil {
			return false, err
		}
		installs, err := client.ListInstallations(ctx, jwt)
		if err != nil {
			return false, err
		}
		for _, install := range installs {
			if _, ok := known[install.ID]; !ok {
				picked = install
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return err
	}

	app.InstallationID = picked.ID
	app.InstallationAccount = picked.Account.Login
	// Installation tokens only reach repos owned by the installed
	// account. Surface uncovered repos before saving: config
	// validation would reject the entry anyway, and the user needs to
	// know the GitHub-side installation exists but was not recorded.
	if uncovered := reposNotCoveredByInstallation(cfg, app); len(uncovered) > 0 {
		return fmt.Errorf(
			"the app was installed on %q on GitHub, but that installation cannot reach "+
				"configured repos %s; not recording it in config. Uninstall it in the "+
				"browser and install on the account that owns those repos, or remove "+
				"them from middleman's config before using an app on this host "+
				"(middleman resolves one credential chain per host, so per-repo token "+
				"overrides cannot mix with an app)",
			picked.Account.Login, strings.Join(uncovered, ", "),
		)
	}
	// Account ownership is not enough for an "Only select repositories"
	// install: the token reaches only the chosen repos, and anything
	// else 404s during sync while the config looks healthy.
	if !strings.EqualFold(picked.RepositorySelection, "all") {
		if err := env.verifySelectedInstallationCoverage(ctx, cfg, app, picked); err != nil {
			return err
		}
	}
	if err := updateAppInConfig(cfg, env.configPath, app); err != nil {
		return fmt.Errorf("saving installation to config: %w", err)
	}
	fmt.Fprintf(env.stdout,
		"Installed on %s (installation %d). middleman will now sync %s with app tokens.\n",
		picked.Account.Login, picked.ID, app.Host,
	)
	return nil
}

// reposNotCoveredByInstallation lists configured github repos on the
// app's host that would resolve to the app token but are owned by a
// different account than the installation. Mirrors the config-level
// coverage validation so the CLI can explain the problem instead of
// failing a save.
// verifySelectedInstallationCoverage checks an "Only select
// repositories" installation against the configured repos it is
// supposed to serve, by listing what an installation token can
// actually reach. Glob repo patterns cannot be verified repo-by-repo
// and are rejected outright: they expand to an open-ended set only an
// "All repositories" install can satisfy.
func (env *appEnv) verifySelectedInstallationCoverage(
	ctx context.Context,
	cfg *config.Config,
	app config.GitHubAppConfig,
	picked githubapp.Installation,
) error {
	client := env.apiClient(app.Host)
	jwt, err := appJWT(app, env.now())
	if err != nil {
		return err
	}
	token, err := client.CreateInstallationToken(ctx, jwt, picked.ID)
	if err != nil {
		return fmt.Errorf("verifying selected-repository installation: %w", err)
	}
	names, err := client.ListInstallationRepositories(ctx, token.Token)
	if err != nil {
		return fmt.Errorf("verifying selected-repository installation: %w", err)
	}
	missing := missingSelectedRepos(cfg, app.Host, picked.Account.Login, names)
	if len(missing) > 0 {
		return fmt.Errorf(
			"the app was installed on %q with \"Only select repositories\", but the "+
				"installation cannot reach %s; not recording it in config. Edit the "+
				"installation's repository access on GitHub (or choose \"All "+
				"repositories\") and re-run \"install\"",
			picked.Account.Login, strings.Join(missing, ", "),
		)
	}
	return nil
}

func reposNotCoveredByInstallation(
	cfg *config.Config, app config.GitHubAppConfig,
) []string {
	var uncovered []string
	for _, r := range cfg.Repos {
		if r.PlatformOrDefault() != "github" || r.PlatformHostOrDefault() != app.Host {
			continue
		}
		if r.TokenEnv != "" || r.TokenFile != "" {
			continue
		}
		if strings.EqualFold(r.Owner, app.InstallationAccount) {
			continue
		}
		uncovered = append(uncovered, r.Owner+"/"+r.Name)
	}
	return uncovered
}

// validAppSlug matches GitHub's app slug shape (letters, digits,
// hyphens). The slug arrives from the manifest conversion response
// and is used as a filename, so anything else — path separators,
// dots, traversal — is rejected rather than written.
var validAppSlug = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)

// validKeyFileHost limits the host's contribution to the key
// filename to hostname-shaped characters. Hosts are normalized by
// config loading, but the filename must stay safe even if that
// changes.
var validKeyFileHost = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)

// writePrivateKey stores the app's PEM next to the config file with
// owner-only permissions and returns its absolute path. The path is
// absolute so later config loads do not re-resolve it against the
// config directory (a relative --config like tmp/config.toml would
// otherwise turn "tmp/x.pem" into "tmp/tmp/x.pem"). The filename
// carries the host because slugs are only unique per host: two apps
// with the same slug on different hosts must not share a key file.
// The slug is untrusted input from the manifest conversion response;
// a malicious GHES host must not be able to steer the write outside
// the config directory.
func writePrivateKey(configPath, host, slug, pem string) (string, error) {
	if !validAppSlug.MatchString(slug) {
		return "", fmt.Errorf(
			"refusing to use app slug %q as a filename: slugs contain only letters, digits, and hyphens",
			slug,
		)
	}
	if !validKeyFileHost.MatchString(host) {
		return "", fmt.Errorf("refusing to use host %q in a key filename", host)
	}
	dir, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return "", fmt.Errorf("resolving config directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating config directory: %w", err)
	}
	name := "github-app-" + strings.ReplaceAll(host, ":", "_") + "-" + slug + ".pem"
	path := filepath.Join(dir, name)
	if filepath.Dir(path) != dir {
		return "", fmt.Errorf("app key path %q escapes the config directory", path)
	}
	if err := os.WriteFile(path, []byte(pem), 0o600); err != nil {
		return "", fmt.Errorf("writing app private key: %w", err)
	}
	return path, nil
}

func randomToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating state token: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// registerTestFlags exposes the endpoint overrides the e2e tests use
// to point both browser-facing and API URLs at a fake GitHub.
func registerTestFlags(fs *flag.FlagSet, env *appEnv) {
	fs.StringVar(&env.apiBase, "api-base", env.apiBase,
		"override the GitHub REST API base URL (testing)")
	fs.StringVar(&env.webBase, "web-base", env.webBase,
		"override the GitHub web base URL (testing)")
}
