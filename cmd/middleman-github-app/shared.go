package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/githubapp"
	"go.kenn.io/middleman/internal/platform"
)

func (env *appEnv) loadConfig() (*config.Config, error) {
	cfg, err := config.Load(env.configPath)
	if err != nil {
		return nil, fmt.Errorf("loading middleman config %s: %w", env.configPath, err)
	}
	return cfg, nil
}

func (env *appEnv) apiClient(host string) *githubapp.Client {
	if env.apiBase != "" {
		return githubapp.NewClientWithBase(env.apiBase)
	}
	return githubapp.NewClient(host)
}

func (env *appEnv) webBaseFor(host string) string {
	if env.webBase != "" {
		return strings.TrimRight(env.webBase, "/")
	}
	return githubapp.WebBaseForHost(host)
}

// selectApp picks the configured app for host. With one configured
// app and no --host flag, that app is selected.
func selectApp(cfg *config.Config, host string) (config.GitHubAppConfig, error) {
	if host == "" {
		switch len(cfg.GitHubApps) {
		case 0:
			return config.GitHubAppConfig{}, fmt.Errorf(
				"no github apps configured; run \"middleman-github-app create\" first",
			)
		case 1:
			return cfg.GitHubApps[0], nil
		default:
			return config.GitHubAppConfig{}, fmt.Errorf(
				"multiple github apps configured; pass --host to pick one",
			)
		}
	}
	app, ok := cfg.GitHubAppForHost(host)
	if !ok {
		return config.GitHubAppConfig{}, fmt.Errorf("no github app configured for host %q", host)
	}
	return app, nil
}

func appJWT(app config.GitHubAppConfig, now time.Time) (string, error) {
	key, err := githubapp.LoadPrivateKey(app.PrivateKeyPath)
	if err != nil {
		return "", err
	}
	return githubapp.SignAppJWT(app.AppID, key, now)
}

// settingsURL is the app's GitHub management page; deletion lives
// under /advanced. Org-owned apps nest under the organization.
func settingsURL(webBase string, app config.GitHubAppConfig) string {
	if strings.EqualFold(app.OwnerType, "Organization") {
		return fmt.Sprintf(
			"%s/organizations/%s/settings/apps/%s", webBase, app.Owner, app.Slug,
		)
	}
	return fmt.Sprintf("%s/settings/apps/%s", webBase, app.Slug)
}

func installURL(webBase string, app config.GitHubAppConfig) string {
	return fmt.Sprintf("%s/apps/%s/installations/new", webBase, app.Slug)
}

// updateAppInConfig replaces the entry for app.Host and saves.
func updateAppInConfig(
	cfg *config.Config, configPath string, app config.GitHubAppConfig,
) error {
	for i := range cfg.GitHubApps {
		if cfg.GitHubApps[i].Host == app.Host {
			cfg.GitHubApps[i] = app
			return cfg.Save(configPath)
		}
	}
	cfg.GitHubApps = append(cfg.GitHubApps, app)
	return cfg.Save(configPath)
}

func removeAppFromConfig(
	cfg *config.Config, configPath, host string,
) error {
	kept := cfg.GitHubApps[:0]
	for _, app := range cfg.GitHubApps {
		if app.Host != host {
			kept = append(kept, app)
		}
	}
	cfg.GitHubApps = kept
	return cfg.Save(configPath)
}

// pollUntil runs probe at the env's poll interval until it reports
// done, the context ends, or timeout elapses.
func (env *appEnv) pollUntil(
	ctx context.Context,
	timeout time.Duration,
	probe func(context.Context) (bool, error),
) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(env.pollInterval)
	defer ticker.Stop()
	for {
		done, err := probe(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out after %s", timeout)
		case <-ticker.C:
		}
	}
}

func normalizeHostFlag(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return platform.DefaultGitHubHost
	}
	return host
}
