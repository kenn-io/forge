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
	"go.kenn.io/middleman/internal/tokenauth"
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
	// terminal, agents, docs, msgvault) are applied regardless.
	RestartRequired bool `json:"restart_required"`
}

// startupConfigSnapshot is a deep copy of the fields the server binds at
// startup. It is taken once in newServer and compared in applyConfigChange
// to detect drift that the watcher cannot fix without a restart.
type startupConfigSnapshot struct {
	SyncInterval        string
	DefaultPlatformHost string
	Host                string
	Port                int
	BasePath            string
	DataDir             string
	SyncBudgetPerHour   int
	AllowedHosts        []config.HostKey
	TrustReverseProxy   bool
	ProviderHosts       []tokenauth.Key
	// TokenEnvNames is the boot-time baseline of provider token env
	// names (msgvault excluded) used to accumulate runtime strip-env
	// lists; it is not compared for restart-required drift.
	TokenEnvNames []string
	Roborev       config.Roborev
	Tmux          config.Tmux
	Shell         config.Shell
}

func snapshotStartupConfig(cfg *config.Config) startupConfigSnapshot {
	if cfg == nil {
		return startupConfigSnapshot{}
	}
	snap := startupConfigSnapshot{
		SyncInterval:        cfg.SyncInterval,
		DefaultPlatformHost: cfg.DefaultPlatformHost,
		Host:                cfg.Host,
		Port:                cfg.Port,
		BasePath:            cfg.BasePath,
		DataDir:             cfg.DataDir,
		SyncBudgetPerHour:   cfg.SyncBudgetPerHour,
		AllowedHosts:        startupAllowedHosts(cfg),
		TrustReverseProxy:   cfg.TrustReverseProxy,
		ProviderHosts:       startupProviderHosts(cfg),
		Roborev:             cfg.Roborev,
	}
	snap.Tmux.Command = slices.Clone(cfg.Tmux.Command)
	if cfg.Tmux.AgentSessions != nil {
		v := *cfg.Tmux.AgentSessions
		snap.Tmux.AgentSessions = &v
	}
	snap.Shell.Command = slices.Clone(cfg.Shell.Command)
	snap.TokenEnvNames = startupBoundTokenEnvNames(cfg)
	return snap
}

func startupAllowedHosts(cfg *config.Config) []config.HostKey {
	if cfg == nil {
		return nil
	}
	allowed := cfg.ParsedAllowedHosts()
	slices.SortFunc(allowed, func(a, b config.HostKey) int {
		if cmp := strings.Compare(a.Host, b.Host); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Port, b.Port)
	})
	return allowed
}

func startupProviderHosts(cfg *config.Config) []tokenauth.Key {
	if cfg == nil {
		return nil
	}
	seen := make(map[tokenauth.Key]struct{}, len(cfg.Platforms)+len(cfg.Repos)+1)
	out := make([]tokenauth.Key, 0, len(cfg.Platforms)+len(cfg.Repos)+1)
	add := func(platformName, host string) {
		key := tokenauth.Key{Platform: platformName, Host: host}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	for _, p := range cfg.Platforms {
		add(p.Type, p.Host)
	}
	for _, r := range cfg.Repos {
		add(r.PlatformOrDefault(), r.PlatformHostOrDefault())
	}
	add(string(platform.KindGitHub), platform.DefaultGitHubHost)
	slices.SortFunc(out, func(a, b tokenauth.Key) int {
		if cmp := strings.Compare(a.Platform, b.Platform); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Host, b.Host)
	})
	return out
}

func startupBoundTokenEnvNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	withoutMsgvault := *cfg
	withoutMsgvault.Msgvault = nil
	names := withoutMsgvault.TokenEnvNames()
	slices.Sort(names)
	return names
}

func initialRuntimeStripEnvNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	names := cfg.TokenEnvNames()
	slices.Sort(names)
	return names
}

func runtimeStripEnvNamesForConfig(
	boot startupConfigSnapshot,
	current []string,
	cfg *config.Config,
) []string {
	names := slices.Clone(boot.TokenEnvNames)
	for _, name := range current {
		if name == "" || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
	}
	for _, name := range cfg.TokenEnvNames() {
		if name == "" || slices.Contains(names, name) {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (s *Server) updateRuntimeStripEnvVarsLocked(cfg *config.Config) []string {
	s.runtimeStripEnvVars = runtimeStripEnvNamesForConfig(
		s.bootCfgSnapshot,
		s.runtimeStripEnvVars,
		cfg,
	)
	return slices.Clone(s.runtimeStripEnvVars)
}

func (s startupConfigSnapshot) restartRequiredFor(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	candidate := snapshotStartupConfig(cfg)
	// TokenEnvNames is the boot baseline for runtime strip-env
	// accumulation, not a startup binding: token env changes hot-reload
	// through the token sources, so they must not flag a restart.
	s.TokenEnvNames = nil
	candidate.TokenEnvNames = nil
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
	if err := validateReloadCloneTokenSources(newCfg); err != nil {
		slog.Warn(
			"config reload failed clone token validation; keeping last-known-good",
			"path", s.cfgPath,
			"err", err,
		)
		return configChangedEvent{
			Valid: false,
			Error: sanitizeConfigError(err, s.cfgPath),
		}
	}
	if err := s.validateReloadProviderTokenSources(ctx, newCfg); err != nil {
		slog.Warn(
			"config reload failed provider token validation; keeping last-known-good",
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

	s.updateRuntimeStripEnvVars(newCfg)
	s.updateTokenSourcesForReload(newCfg)
	restartRequired := s.bootCfgSnapshot.restartRequiredFor(newCfg)
	if s.reloadCredentialNeedsClientRebuild(ctx, newCfg) {
		restartRequired = true
	}
	warnDocFolderDaemonBindings(newCfg.DocFolders)

	// Resolve the new repo set against the boot-time registry. Repos
	// whose (platform, host) the registry never learned about cannot
	// reach a client without a restart; skip those for SetRepos but
	// keep them in s.cfg so the UI mirrors the file. A server built
	// without a syncer (embedded or test setups) still hot-reloads the
	// non-sync surfaces below, so the syncer is nil-guarded rather than
	// treated as a reload failure.
	var previous []ghclient.RepoRef
	if s.syncer != nil {
		previous = s.syncer.TrackedRepos()
	}
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
	s.cfg.GitHubTokenEnv = newCfg.GitHubTokenEnv
	s.cfg.Repos = slices.Clone(newCfg.Repos)
	s.cfg.Platforms = slices.Clone(newCfg.Platforms)
	s.cfg.Activity = newCfg.Activity
	s.cfg.Terminal = newCfg.Terminal
	s.cfg.Modes = cloneModeVisibility(newCfg.Modes)
	s.cfg.Agents = cloneConfigAgents(newCfg.Agents)
	s.cfg.DocFolders = slices.Clone(newCfg.DocFolders)
	s.cfg.Msgvault = cloneMsgvault(newCfg.Msgvault)
	if s.docsRegistry != nil {
		s.docsRegistry.Replace(newCfg.DocFolders)
	}
	if s.msgvault != nil {
		s.msgvault.applyConfig(newCfg)
	}

	if s.syncer != nil {
		s.syncer.SetRepos(resolved)
		s.syncer.SetBranchActivityLimits(
			newCfg.BranchActivityRetention(),
			newCfg.Activity.DefaultBranchMaxCommits,
		)
	}

	s.refreshRuntimeTargetsLocked()
	if s.runtime != nil {
		s.runtime.UpdateStripEnvVars(s.updateRuntimeStripEnvVarsLocked(newCfg))
	}

	slog.Info(
		"config reload applied",
		"path", s.cfgPath,
		"repo_count", len(resolved),
		"restart_required", restartRequired,
	)
	return configChangedEvent{Valid: true, RestartRequired: restartRequired}
}

// reloadCredentialNeedsClientRebuild reports whether the reloaded config
// resolves a token for a configured platform host that has no live provider
// client. Clients are constructed at startup from the sources that resolved
// then, so a credential added for a host that booted credential-less cannot
// serve sync, settings, or import requests until restart — surface that
// instead of reporting a clean hot reload. Hosts whose token still does not
// resolve are skipped: restarting would not make them usable either.
func (s *Server) reloadCredentialNeedsClientRebuild(
	ctx context.Context,
	cfg *config.Config,
) bool {
	if s.syncer == nil || s.tokenSources == nil || cfg == nil {
		return false
	}
	for _, pc := range cfg.Platforms {
		if _, err := s.syncer.RepositoryReader(
			platform.Kind(pc.Type), pc.Host,
		); err == nil {
			continue
		}
		src, ok := s.tokenSources.Get(tokenauth.Key{
			Platform: pc.Type,
			Host:     pc.Host,
		})
		if !ok || src == nil {
			continue
		}
		if _, err := src.Token(ctx); err == nil {
			return true
		}
	}
	return false
}

func (s *Server) updateTokenSourcesForReload(cfg *config.Config) {
	if s.tokenSources == nil || cfg == nil {
		return
	}
	updateIfKnown := func(desc tokenauth.Descriptor) {
		if _, ok := s.tokenSources.Get(desc.Key); !ok {
			return
		}
		s.tokenSources.Upsert(desc)
	}
	for _, plan := range cfg.ProviderTokenSources() {
		updateIfKnown(plan.Descriptor)
	}
	// Clone credentials live under host-level keys (tokenauth.CloneKey), so
	// when a provider entry on a shared host loses or changes its token the
	// clone source follows the host's surviving effective chain instead of
	// staying pinned to whichever provider source startup picked.
	for _, desc := range cfg.CloneTokenDescriptors() {
		updateIfKnown(desc)
	}
}

func (s *Server) validateReloadProviderTokenSources(
	ctx context.Context,
	cfg *config.Config,
) error {
	if s.tokenSources == nil || cfg == nil {
		return nil
	}
	for _, plan := range cfg.ProviderTokenSources() {
		if !plan.Required {
			continue
		}
		desc := plan.Descriptor
		if _, ok := s.tokenSources.Get(desc.Key); !ok {
			continue
		}
		if _, err := s.tokenSources.ProbeToken(ctx, desc); err != nil {
			label := fmt.Sprintf("%s host %s", desc.Key.Platform, desc.Key.Host)
			return fmt.Errorf(
				"no token for %s via %s: %w", label, desc.SafeString(), err,
			)
		}
	}
	return nil
}

func validateReloadCloneTokenSources(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	plans := cfg.ProviderTokenSources()
	byHost := make(map[string]string, len(plans))
	for _, plan := range plans {
		desc := plan.Descriptor
		if desc.Key.Host == "" {
			continue
		}
		// A credential-less platform host imposes no clone credential;
		// comparing its empty chain against tokened entries on the same
		// host would reject configs that are valid at startup.
		if len(desc.Candidates) == 0 {
			continue
		}
		sourceID := desc.CanonicalSourceString()
		if existing, ok := byHost[desc.Key.Host]; ok {
			if existing != sourceID {
				return fmt.Errorf(
					"host %s is configured with different clone token sources; use identical tokens or separate hosts",
					desc.Key.Host,
				)
			}
			continue
		}
		byHost[desc.Key.Host] = sourceID
	}
	return nil
}

func cloneMsgvault(in *config.Msgvault) *config.Msgvault {
	if in == nil {
		return nil
	}
	out := *in
	return &out
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
	return tokenauth.RedactKnownSecrets(msg)
}
