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

// loadConfig loads with GitHub App coverage validation relaxed: every
// command of this CLI is a repair path for exactly the configs that
// strict loading rejects (stale selected snapshot, app on the wrong
// account), so a coverage failure must never lock the user out of
// install/uninstall/delete. middleman itself still loads strictly.
func (env *appEnv) loadConfig() (*config.Config, error) {
	cfg, err := config.LoadForGitHubAppRepair(env.configPath)
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

// selectApp picks a configured app credential. Same-host configs can carry
// multiple private apps, so management commands must disambiguate by owner when
// a host alone is not unique.
func selectApp(cfg *config.Config, host, owner string) (config.GitHubAppConfig, error) {
	if cfg == nil || len(cfg.GitHubApps) == 0 {
		return config.GitHubAppConfig{}, fmt.Errorf(
			"no github apps configured; run \"middleman-github-app create\" first",
		)
	}
	owner = strings.TrimSpace(owner)
	var matches []config.GitHubAppConfig
	for _, app := range cfg.GitHubApps {
		if host != "" && app.Host != normalizeHostFlag(host) {
			continue
		}
		if owner != "" &&
			!strings.EqualFold(app.Owner, owner) &&
			!strings.EqualFold(app.InstallationAccount, owner) {
			continue
		}
		matches = append(matches, app)
	}
	if len(matches) == 0 {
		if host != "" && owner != "" {
			return config.GitHubAppConfig{}, fmt.Errorf(
				"no github app configured for host %q and owner %q", normalizeHostFlag(host), owner,
			)
		}
		if host != "" {
			return config.GitHubAppConfig{}, fmt.Errorf("no github app configured for host %q", normalizeHostFlag(host))
		}
		return config.GitHubAppConfig{}, fmt.Errorf("no github app configured for owner %q", owner)
	}
	if len(matches) > 1 {
		return config.GitHubAppConfig{}, fmt.Errorf(
			"multiple github apps match; pass --owner to select one",
		)
	}
	return matches[0], nil
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

// updateAppInConfig replaces the matching app credential and saves. Same-host
// entries are distinct apps, usually one private app per GitHub account.
func updateAppInConfig(
	cfg *config.Config, configPath string, app config.GitHubAppConfig,
) error {
	for i := range cfg.GitHubApps {
		existing := cfg.GitHubApps[i]
		if existing.Host != app.Host {
			continue
		}
		sameAppID := existing.AppID == app.AppID
		sameOwner := existing.Owner != "" && app.Owner != "" &&
			strings.EqualFold(existing.Owner, app.Owner)
		sameInstallation := existing.InstallationAccount != "" &&
			app.InstallationAccount != "" &&
			strings.EqualFold(existing.InstallationAccount, app.InstallationAccount)
		if sameAppID || sameOwner || sameInstallation {
			cfg.GitHubApps[i] = app
			return cfg.Save(configPath)
		}
	}
	cfg.GitHubApps = append(cfg.GitHubApps, app)
	return cfg.Save(configPath)
}

func updateAppSlotInConfig(
	cfg *config.Config,
	configPath string,
	oldApp config.GitHubAppConfig,
	newApp config.GitHubAppConfig,
) error {
	for i := range cfg.GitHubApps {
		existing := cfg.GitHubApps[i]
		if existing.Host == oldApp.Host &&
			existing.AppID == oldApp.AppID &&
			strings.EqualFold(existing.InstallationAccount, oldApp.InstallationAccount) {
			cfg.GitHubApps[i] = newApp
			return cfg.Save(configPath)
		}
	}
	return updateAppInConfig(cfg, configPath, newApp)
}

func removeAppFromConfig(cfg *config.Config, configPath string, app config.GitHubAppConfig) error {
	kept := cfg.GitHubApps[:0]
	for _, existing := range cfg.GitHubApps {
		if existing.Host == app.Host && existing.AppID == app.AppID {
			continue
		}
		kept = append(kept, existing)
	}
	cfg.GitHubApps = kept
	return cfg.Save(configPath)
}

// missingSelectedRepos lists configured github repos on host owned by
// account that a "selected repositories" installation cannot reach,
// given the full names its token reported accessible. Repos with
// their own credential override never resolve to the app token and
// are exempt; glob patterns expand to an open-ended set only an "All
// repositories" install can satisfy.
func missingSelectedRepos(
	cfg *config.Config, host, account string, accessible []string,
) []string {
	reachable := make(map[string]struct{}, len(accessible))
	for _, name := range accessible {
		reachable[strings.ToLower(name)] = struct{}{}
	}
	var missing []string
	for _, r := range cfg.Repos {
		if r.PlatformOrDefault() != "github" || r.PlatformHostOrDefault() != host {
			continue
		}
		if r.TokenEnv != "" || r.TokenFile != "" {
			continue
		}
		if !strings.EqualFold(r.Owner, account) {
			continue
		}
		full := r.Owner + "/" + r.Name
		if r.HasNameGlob() {
			missing = append(missing, full+" (glob patterns need an \"All repositories\" install)")
			continue
		}
		if _, ok := reachable[strings.ToLower(full)]; !ok {
			missing = append(missing, full)
		}
	}
	return missing
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
