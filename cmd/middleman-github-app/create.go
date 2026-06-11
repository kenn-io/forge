package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/githubapp"
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

	creds, err := env.runManifestFlow(ctx, manifestFlowOptions{
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

	keyPath, err := writePrivateKey(env.configPath, creds.Slug, creds.PEM)
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

// callbackPage is what the user sees in the browser tab after GitHub
// redirects back; the interesting work continues in the terminal.
const callbackPage = `<!DOCTYPE html><html><body>
<p>GitHub App created. You can close this tab and return to the terminal.</p>
</body></html>`

var manifestPage = template.Must(template.New("manifest").Parse(
	`<!DOCTYPE html><html><body>
<form id="m" action="{{.Action}}" method="post">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<noscript><button type="submit">Create GitHub App {{.Name}}</button></noscript>
</form>
<script>document.getElementById("m").submit();</script>
</body></html>`))

// runManifestFlow serves the manifest form on loopback, sends the
// user's browser to it, and exchanges the code GitHub redirects back
// with for the new app's credentials.
func (env *appEnv) runManifestFlow(
	ctx context.Context, opts manifestFlowOptions,
) (*githubapp.AppCredentials, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting local callback server: %w", err)
	}
	defer listener.Close()
	localBase := "http://" + listener.Addr().String()

	state, err := randomToken()
	if err != nil {
		return nil, err
	}
	// The callback path is itself unguessable, so a forged request
	// cannot hit the handler even if the state echo were ever dropped.
	callbackPath := "/callback/" + state
	manifest, err := githubapp.NewManifest(
		opts.name, opts.homepage, localBase+callbackPath,
	)
	if err != nil {
		return nil, err
	}
	manifestJSON, err := manifest.JSON()
	if err != nil {
		return nil, err
	}

	action := env.webBaseFor(opts.host) + "/settings/apps/new?state=" + state
	if opts.org != "" {
		action = env.webBaseFor(opts.host) +
			"/organizations/" + opts.org + "/settings/apps/new?state=" + state
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = manifestPage.Execute(w, map[string]string{
			"Action":   action,
			"Manifest": manifestJSON,
			"Name":     opts.name,
		})
	})
	mux.HandleFunc("GET "+callbackPath, func(w http.ResponseWriter, r *http.Request) {
		gotState := r.URL.Query().Get("state")
		if gotState != "" &&
			subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, callbackPage)
		select {
		case codeCh <- code:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	defer srv.Close()

	fmt.Fprintf(env.stdout,
		"Open this page to create the app (it submits a prepared manifest to GitHub):\n  %s\n",
		localBase,
	)
	if opts.open {
		if err := env.openBrowser(localBase); err != nil {
			fmt.Fprintf(env.stdout, "could not open browser: %v\n", err)
		}
	}
	fmt.Fprintln(env.stdout, "Waiting for GitHub to redirect back after you click Create...")

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
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
	var picked *githubapp.Installation
	err := env.pollUntil(ctx, timeout, func(ctx context.Context) (bool, error) {
		jwt, err := appJWT(app, env.now())
		if err != nil {
			return false, err
		}
		installs, err := client.ListInstallations(ctx, jwt)
		if err != nil {
			return false, err
		}
		for i := range installs {
			if _, ok := known[installs[i].ID]; !ok {
				picked = &installs[i]
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
	if err := updateAppInConfig(cfg, env.configPath, app); err != nil {
		return fmt.Errorf("saving installation to config: %w", err)
	}
	fmt.Fprintf(env.stdout,
		"Installed on %s (installation %d). middleman will now sync %s with app tokens.\n",
		picked.Account.Login, picked.ID, app.Host,
	)
	return nil
}

// writePrivateKey stores the app's PEM next to the config file with
// owner-only permissions and returns its path.
func writePrivateKey(configPath, slug, pem string) (string, error) {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating config directory: %w", err)
	}
	path := filepath.Join(dir, "github-app-"+slug+".pem")
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
