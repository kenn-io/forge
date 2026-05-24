package server

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/configwatch"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
)

// configChangedEvent is the payload broadcast on the SSE "config.changed"
// channel. A late subscriber receives the most recently broadcast event so
// it can detect a stale-config state even if it connected after the parse
// error landed.
type configChangedEvent struct {
	// Valid reports whether the reloaded file parsed and validated. The
	// daemon keeps the previous in-memory config when Valid is false.
	Valid bool `json:"valid"`
	// Error is set when Valid is false; it contains a sanitized message
	// derived from the config parser/validator.
	Error string `json:"error,omitempty"`
	// RestartRequired is true when one or more startup-bound fields
	// (listener address, base path, sync interval, data dir, token env
	// names, platform registry, tmux/shell command, etc.) differ from
	// the boot-time snapshot. Hot-reloadable fields (repos, activity,
	// terminal, agents) are applied regardless.
	RestartRequired bool `json:"restart_required"`
}

// startupConfigSnapshot is a deep copy of the fields the server binds at
// startup. It is taken once in newServer and compared in applyConfigChange
// to detect drift that the watcher cannot fix without a restart.
type startupConfigSnapshot struct {
	SyncInterval        string
	GitHubTokenEnv      string
	DefaultPlatformHost string
	Host                string
	Port                int
	BasePath            string
	DataDir             string
	SyncBudgetPerHour   int
	Platforms           []config.PlatformConfig
	Roborev             config.Roborev
	Tmux                config.Tmux
	Shell               config.Shell
	TokenEnvNames       []string
}

func snapshotStartupConfig(cfg *config.Config) startupConfigSnapshot {
	if cfg == nil {
		return startupConfigSnapshot{}
	}
	snap := startupConfigSnapshot{
		SyncInterval:        cfg.SyncInterval,
		GitHubTokenEnv:      cfg.GitHubTokenEnv,
		DefaultPlatformHost: cfg.DefaultPlatformHost,
		Host:                cfg.Host,
		Port:                cfg.Port,
		BasePath:            cfg.BasePath,
		DataDir:             cfg.DataDir,
		SyncBudgetPerHour:   cfg.SyncBudgetPerHour,
		Platforms:           slices.Clone(cfg.Platforms),
		Roborev:             cfg.Roborev,
	}
	snap.Tmux.Command = slices.Clone(cfg.Tmux.Command)
	if cfg.Tmux.AgentSessions != nil {
		v := *cfg.Tmux.AgentSessions
		snap.Tmux.AgentSessions = &v
	}
	snap.Shell.Command = slices.Clone(cfg.Shell.Command)
	snap.TokenEnvNames = cfg.TokenEnvNames()
	slices.Sort(snap.TokenEnvNames)
	return snap
}

func (s startupConfigSnapshot) restartRequiredFor(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	candidate := snapshotStartupConfig(cfg)
	// Reflect-deep-equal handles slice and pointer comparison correctly
	// here; the snapshot owns its own slices so external mutation cannot
	// blur the comparison.
	return !reflect.DeepEqual(s, candidate)
}

// startConfigWatcher initializes the fsnotify-based watcher. It is a noop
// when cfgPath is empty (tests that build a Server without persistence) or
// when the parent directory of cfgPath does not exist on disk. The
// watcher goroutine is registered with runBackground so Shutdown waits
// for it to drain.
func (s *Server) startConfigWatcher() {
	if s.cfgPath == "" {
		return
	}
	w, err := configwatch.New(configwatch.Options{
		Path:     s.cfgPath,
		OnChange: s.handleConfigFileChanged,
	})
	if err != nil {
		slog.Warn("config watcher init failed", "err", err)
		return
	}
	s.configWatcher = w
	if !s.runBackground(func(ctx context.Context) {
		w.Start(ctx)
		<-w.Done()
	}) {
		// Shutdown started before we could schedule the watcher; the
		// daemon is on its way out, nothing more to do.
		s.configWatcher = nil
	}
}

// handleConfigFileChanged is invoked by the watcher after debouncing a
// burst of fsnotify events on the config file. It reloads the file,
// applies hot-reloadable fields, and broadcasts a config.changed SSE
// event. The daemon stays running on the previous in-memory config when
// the reload fails so an editor mid-save cannot crash the process.
func (s *Server) handleConfigFileChanged() {
	if s.cfgPath == "" {
		return
	}
	s.configReloadMu.Lock()
	defer s.configReloadMu.Unlock()

	event := s.applyConfigChange(s.bgCtx)
	s.hub.Broadcast(Event{
		Type: "config.changed",
		Data: event,
	})
}

// applyConfigChange reloads the config file, copies hot-reloadable fields
// onto the in-memory config, refreshes the syncer's repo set and runtime
// targets, and returns the payload to broadcast. Repository expansion can
// touch provider clients, so it happens before taking cfgMu. The lock is
// held only while applying the already-resolved result to in-memory state.
// The SSE broadcast is intentionally moved out of this function (to
// handleConfigFileChanged) so a slow subscriber cannot stall the daemon.
func (s *Server) applyConfigChange(ctx context.Context) configChangedEvent {
	newCfg, err := config.Load(s.cfgPath)
	if err != nil {
		slog.Warn(
			"config reload failed; keeping last-known-good",
			"path", s.cfgPath,
			"err", err,
		)
		return configChangedEvent{
			Valid: false,
			Error: sanitizeConfigError(err, s.cfgPath),
		}
	}

	s.cfgMu.Lock()
	if s.cfg == nil {
		s.cfgMu.Unlock()
		// Defensive: a Server constructed without a cfg cannot be hot
		// reloaded; treat the change as a parse error so subscribers
		// learn nothing useful was applied.
		return configChangedEvent{
			Valid: false,
			Error: "config reload disabled: server has no in-memory config",
		}
	}
	s.cfgMu.Unlock()

	restartRequired := s.bootCfgSnapshot.restartRequiredFor(newCfg)

	// Resolve the new repo set against the boot-time registry. Repos
	// whose (platform, host) the registry never learned about cannot
	// reach a client without a restart; skip those for SetRepos but
	// keep them in s.cfg so the UI mirrors the file.
	previous := s.syncer.TrackedRepos()
	resolved, skipped := s.resolveReposForReload(ctx, newCfg.Repos, previous)
	if len(skipped) > 0 {
		slog.Info(
			"config reload: skipping repos for unknown platform hosts",
			"path", s.cfgPath,
			"skipped", skipped,
		)
		restartRequired = true
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	// Apply hot-reloadable fields. Repos and Platforms are deep-copied
	// so subsequent in-memory mutations from the Settings UI cannot
	// surprise the file's last view.
	s.cfg.Repos = slices.Clone(newCfg.Repos)
	s.cfg.Platforms = slices.Clone(newCfg.Platforms)
	s.cfg.Activity = newCfg.Activity
	s.cfg.Terminal = newCfg.Terminal
	s.cfg.Agents = cloneConfigAgents(newCfg.Agents)

	s.syncer.SetRepos(resolved)
	s.syncer.SetBranchActivityLimits(
		newCfg.BranchActivityRetention(),
		newCfg.Activity.DefaultBranchMaxCommits,
	)

	s.refreshRuntimeTargetsLocked()

	slog.Info(
		"config reload applied",
		"path", s.cfgPath,
		"repo_count", len(resolved),
		"restart_required", restartRequired,
	)
	return configChangedEvent{Valid: true, RestartRequired: restartRequired}
}

// resolveReposForReload walks the reloaded repo list and asks the syncer
// (via the boot-time platform registry) whether each (provider, host)
// pair has a client. Known hosts are resolved into RepoRefs; unknown
// hosts are returned in the skipped slice with a "owner/name@host"
// display string for logging.
func (s *Server) resolveReposForReload(
	ctx context.Context,
	repos []config.Repo,
	previous []ghclient.RepoRef,
) ([]ghclient.RepoRef, []string) {
	if s.syncer == nil {
		return nil, nil
	}
	resolved := make([]ghclient.RepoRef, 0, len(repos))
	seen := make(map[string]struct{}, len(repos))
	skipped := make([]string, 0)

	for _, raw := range repos {
		host := raw.PlatformHostOrDefault()
		kind := platform.Kind(raw.PlatformOrDefault())
		if _, err := s.syncer.RepositoryReader(kind, host); err != nil {
			skipped = append(skipped, fmt.Sprintf(
				"%s/%s@%s/%s",
				string(kind), host, raw.Owner, raw.Name,
			))
			continue
		}
		_, expanded, err := ghclient.ResolveConfiguredRepoWithRegistry(
			ctx, s.syncer.Registry(), raw,
		)
		if err != nil {
			// Network failure or transient API error: fall back to a
			// synthetic RepoRef built from the configured fields so
			// the syncer still has a target to retry on its next
			// tick. This matches resolveStartupRepos's offline-
			// resilience behavior.
			slog.Warn(
				"config reload resolve repo failed; using fallback",
				"owner", raw.Owner,
				"name", raw.Name,
				"err", err,
			)
			expanded = ghclient.FallbackConfiguredRepoRefs(previous, raw)
		}
		for _, repo := range expanded {
			key := strings.ToLower(string(repoPlatformOrDefault(repo))) + "\x00" +
				strings.ToLower(canonicalReloadHost(repo)) + "\x00" +
				strings.ToLower(repo.Owner) + "\x00" +
				strings.ToLower(repo.Name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			resolved = append(resolved, repo)
		}
	}
	return resolved, skipped
}

func repoPlatformOrDefault(repo ghclient.RepoRef) platform.Kind {
	if repo.Platform != "" {
		return repo.Platform
	}
	return platform.KindGitHub
}

func canonicalReloadHost(repo ghclient.RepoRef) string {
	if repo.PlatformHost != "" {
		return repo.PlatformHost
	}
	if host, ok := platform.DefaultHost(repoPlatformOrDefault(repo)); ok {
		return host
	}
	return platform.DefaultGitHubHost
}

// sanitizeConfigError trims internal path prefixes from the error so the
// frontend payload does not leak the absolute config path on the user's
// machine. The path is already known to the operator from logs.
func sanitizeConfigError(err error, cfgPath string) string {
	msg := err.Error()
	if cfgPath != "" {
		msg = strings.ReplaceAll(msg, cfgPath, "config.toml")
	}
	return msg
}
