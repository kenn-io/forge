package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type settingsResponse struct {
	Repos         []ghclient.ConfiguredRepoStatus `json:"repos" nullable:"false"`
	Activity      config.Activity                 `json:"activity"`
	PullRequests  config.PullRequests             `json:"pull_requests"`
	Workspaces    config.Workspaces               `json:"workspaces"`
	Issues        config.Issues                   `json:"issues"`
	Notifications notificationsSettingsResponse   `json:"notifications"`
	Terminal      config.Terminal                 `json:"terminal"`
	Modes         config.ModeVisibility           `json:"modes,omitzero"`
	Agents        []config.Agent                  `json:"agents" nullable:"false"`
	KataProjects  []config.KataProjectRepoMapping `json:"kata_projects" nullable:"false"`
	LaunchTargets []localruntime.LaunchTarget     `json:"launch_targets,omitempty"`
	Fleet         fleetSettingsResponse           `json:"fleet"`
}

type notificationsSettingsResponse struct {
	Enabled bool `json:"enabled"`
}

type updateSettingsRequest struct {
	Activity     *config.Activity                 `json:"activity,omitempty"`
	PullRequests *config.PullRequests             `json:"pull_requests,omitempty"`
	Workspaces   *config.Workspaces               `json:"workspaces,omitempty"`
	Issues       *config.Issues                   `json:"issues,omitempty"`
	Terminal     *config.Terminal                 `json:"terminal,omitempty"`
	Modes        *config.ModeVisibility           `json:"modes,omitempty"`
	Agents       *[]config.Agent                  `json:"agents,omitempty"`
	KataProjects *[]config.KataProjectRepoMapping `json:"kata_projects,omitempty"`
}

func (s *Server) configuredClients(
	repos []config.Repo,
) map[string]ghclient.Client {
	clients := make(map[string]ghclient.Client)
	for _, repo := range repos {
		host := repo.PlatformHostOrDefault()
		if _, ok := clients[host]; ok {
			continue
		}
		client, err := s.syncer.ClientForHost(host)
		if err != nil {
			continue
		}
		clients[host] = client
	}
	return clients
}

// buildLocalSettingsResponse builds the settings response from in-memory
// state (syncer tracked repos) plus the hidden-from-UI preferences persisted
// in SQLite, without calling the provider.
func (s *Server) buildLocalSettingsResponse(
	ctx context.Context,
) (settingsResponse, error) {
	s.cfgMu.Lock()
	repos := slices.Clone(s.cfg.Repos)
	activity := s.cfg.Activity
	pullRequests := s.cfg.PullRequests
	workspaces := s.cfg.Workspaces
	issues := s.cfg.Issues
	terminal := s.cfg.Terminal
	modes := cloneModeVisibility(s.cfg.Modes).WithDefaults()
	agents := cloneConfigAgents(s.cfg.Agents)
	kataProjects := slices.Clone(s.cfg.KataProjects)
	if kataProjects == nil {
		// kata_projects is a required non-null array in the API schema, so a
		// nil clone (the default, no-mappings case) must serialize as [] rather
		// than null.
		kataProjects = []config.KataProjectRepoMapping{}
	}
	tmuxCommand := s.cfg.TmuxCommand()
	fleetSettings := s.buildFleetSettingsResponseLocked()
	s.cfgMu.Unlock()
	launchTargets := localruntime.ResolveLaunchTargets(agents, tmuxCommand, nil)
	if launchTargets == nil {
		launchTargets = []localruntime.LaunchTarget{}
	}

	hiddenSet, err := s.hiddenRepoCorrelationSet(ctx)
	if err != nil {
		return settingsResponse{}, err
	}
	var tracked []ghclient.RepoRef
	if s.syncer != nil {
		tracked = s.syncer.TrackedRepos()
	}
	configured := make(
		[]ghclient.ConfiguredRepoStatus, len(repos),
	)
	for i, raw := range repos {
		hiddenFromUI, err := s.configEntryHidden(ctx, raw, tracked, hiddenSet)
		if err != nil {
			return settingsResponse{}, err
		}
		configured[i] = ghclient.ConfiguredRepoStatus{
			Provider:         raw.PlatformOrDefault(),
			PlatformHost:     raw.PlatformHostOrDefault(),
			Owner:            raw.Owner,
			Name:             raw.Name,
			RepoPath:         configRepoPath(raw),
			TrackedRepoPath:  trackedPathForConfig(raw, tracked),
			WorktreeBasePath: raw.WorktreeBasePath,
			IsGlob:           raw.HasNameGlob(),
			MatchedRepoCount: matchedRepoCount(raw, tracked),
			HiddenFromUI:     hiddenFromUI,
		}
	}
	return settingsResponse{
		Repos:        configured,
		Activity:     activity,
		PullRequests: pullRequests,
		Workspaces:   workspaces,
		Issues:       issues,
		// Notifications are a built-in capability with no enable/disable
		// setting; report them as always available.
		Notifications: notificationsSettingsResponse{Enabled: true},
		Terminal:      terminal,
		Modes:         modes,
		Agents:        agents,
		KataProjects:  kataProjects,
		LaunchTargets: launchTargets,
		Fleet:         fleetSettings,
	}, nil
}

// hiddenRepoCorrelation carries the two addresses of every catalog row with a
// hidden-from-UI preference: stable provider identity keys for correlating
// tracked refs, and catalog row ids for entries whose tracked stable identity
// is unavailable.
type hiddenRepoCorrelation struct {
	keys map[string]struct{}
	ids  map[int64]struct{}
}

// hiddenRepoCorrelationSet returns the identity keys and catalog row ids of
// repositories with a hidden-from-UI preference, for correlating configured
// entries with their tracked repositories. Routes are mutable and reusable, so
// correlation must never key on them: a displaced row keeps its old display
// route, and a replacement repository at that route is a different repository.
func (s *Server) hiddenRepoCorrelationSet(
	ctx context.Context,
) (hiddenRepoCorrelation, error) {
	if s.db == nil {
		return hiddenRepoCorrelation{}, nil
	}
	hidden, err := s.db.HiddenRepos(ctx)
	if err != nil {
		return hiddenRepoCorrelation{}, fmt.Errorf("list hidden repos: %w", err)
	}
	set := hiddenRepoCorrelation{
		keys: make(map[string]struct{}, len(hidden)),
		ids:  make(map[int64]struct{}, len(hidden)),
	}
	for _, repo := range hidden {
		set.ids[repo.ID] = struct{}{}
		key := trackedRepoIdentityKey(ghclient.RepoRef{
			Platform:           httpapi.ProviderKind(repo),
			PlatformHost:       httpapi.ProviderHost(repo),
			PlatformExternalID: repo.PlatformRepoID,
		})
		if key == "" {
			continue
		}
		set.keys[key] = struct{}{}
	}
	return set, nil
}

// configEntryHidden reports whether the exact configured entry's repository
// carries a hidden-from-UI preference. Glob entries have no visibility of
// their own: the preference belongs to exact repositories. Tracked refs with
// a stable provider identity answer directly; without one (a route-only ref
// or a server without a syncer), the entry resolves to its catalog row the
// same way the mutation path does.
func (s *Server) configEntryHidden(
	ctx context.Context,
	raw config.Repo,
	tracked []ghclient.RepoRef,
	hidden hiddenRepoCorrelation,
) (bool, error) {
	if raw.HasNameGlob() || len(hidden.ids) == 0 {
		return false, nil
	}
	for _, repo := range tracked {
		if !repoMatchesConfig(repo, raw) {
			continue
		}
		key := trackedRepoIdentityKey(repo)
		if key == "" {
			continue
		}
		_, ok := hidden.keys[key]
		return ok, nil
	}
	repo, err := s.lookupRepoForVisibilityRelease(
		ctx, s.visibilityLookupIdentity(raw),
	)
	if err != nil {
		return false, fmt.Errorf(
			"resolve configured repo %s for hidden state: %w",
			configRepoPath(raw), err,
		)
	}
	if repo == nil {
		return false, nil
	}
	_, ok := hidden.ids[repo.ID]
	return ok, nil
}

// trackedPathForConfig returns the provider-verified current route of the
// tracked repository backing an exact configured entry, or empty for globs
// and untracked entries. Renames move the route while the entry keeps its
// configured address, and clients release route-keyed state through this
// value.
func trackedPathForConfig(
	raw config.Repo, tracked []ghclient.RepoRef,
) string {
	if raw.HasNameGlob() {
		return ""
	}
	for _, repo := range tracked {
		if repoMatchesConfig(repo, raw) {
			return trackedRepoPath(repo)
		}
	}
	return ""
}

func matchedRepoCount(
	raw config.Repo, tracked []ghclient.RepoRef,
) int {
	host := raw.PlatformHostOrDefault()
	provider := raw.PlatformOrDefault()
	count := 0
	for _, repo := range tracked {
		if !strings.EqualFold(repoProvider(repo), provider) ||
			!samePlatformHost(repo.PlatformHost, host) ||
			!strings.EqualFold(repo.Owner, raw.Owner) {
			continue
		}
		if raw.HasNameGlob() {
			matched, _ := path.Match(
				strings.ToLower(raw.Name),
				strings.ToLower(repo.Name),
			)
			if matched {
				count++
			}
		} else if strings.EqualFold(trackedRepoPath(repo), configRepoPath(raw)) ||
			strings.EqualFold(repo.Name, raw.Name) {
			count++
		}
	}
	return count
}

// mergeTrackedRepos adds repos to the syncer's tracked set, deduplicating by
// stable provider id when present and host/owner/name otherwise. An
// already-tracked repo takes the freshly resolved metadata so provider state
// transitions (renames, archived flips) apply without a daemon restart.
func (s *Server) mergeTrackedRepos(add []ghclient.RepoRef) {
	current := s.syncer.TrackedRepos()
	provenance := trackedRepoProvenance(current)
	byRoute := make(map[string]int, len(current))
	byIdentity := make(map[string]int, len(current))
	for i, r := range current {
		indexTrackedRepo(byRoute, byIdentity, r, i)
	}
	for _, r := range add {
		r = withTrackedProvenance(provenance, r)
		if i, ok := trackedRepoIndex(byRoute, byIdentity, r); ok {
			unindexTrackedRepo(byRoute, byIdentity, current[i])
			current[i] = r
			indexTrackedRepo(byRoute, byIdentity, r, i)
			continue
		}
		indexTrackedRepo(byRoute, byIdentity, r, len(current))
		current = append(current, r)
	}
	s.syncer.SetRepos(current)
}

// replaceGlobRepos removes repos that only match the refreshed
// glob entry, preserves repos still matched by other config
// entries, then adds the newly resolved matches.
func (s *Server) replaceGlobRepos(
	raw config.Repo,
	expanded []ghclient.RepoRef,
	configured []config.Repo,
) {
	current := s.syncer.TrackedRepos()
	provenance := trackedRepoProvenance(current)
	kept := make([]ghclient.RepoRef, 0, len(current))
	byRoute := make(map[string]int, len(current)+len(expanded))
	byIdentity := make(map[string]int, len(current)+len(expanded))
	for _, repo := range current {
		if repoMatchesConfig(repo, raw) &&
			!repoMatchesOtherConfig(repo, raw, configured) {
			continue
		}
		if _, ok := trackedRepoIndex(byRoute, byIdentity, repo); ok {
			continue
		}
		indexTrackedRepo(byRoute, byIdentity, repo, len(kept))
		kept = append(kept, repo)
	}
	// Freshly resolved matches overwrite refs kept for overlapping config
	// entries so provider state transitions (renames, archived flips) apply.
	for _, repo := range expanded {
		repo = withTrackedProvenance(provenance, repo)
		if i, ok := trackedRepoIndex(byRoute, byIdentity, repo); ok {
			unindexTrackedRepo(byRoute, byIdentity, kept[i])
			kept[i] = repo
			indexTrackedRepo(byRoute, byIdentity, repo, i)
			continue
		}
		indexTrackedRepo(byRoute, byIdentity, repo, len(kept))
		kept = append(kept, repo)
	}
	s.syncer.SetRepos(kept)
}

// removeConfigRepos keeps only tracked repos that match at
// least one of the remaining config entries. A kept repo whose exact-entry
// provenance no longer names a remaining entry loses it: a stale claim
// would bind a future entry with the same path to the wrong repository.
func (s *Server) removeConfigRepos(
	remaining []config.Repo,
) {
	current := s.syncer.TrackedRepos()
	kept := make([]ghclient.RepoRef, 0, len(current))
	for _, repo := range current {
		matched, provenanceRemains := false, false
		for _, raw := range remaining {
			if repoMatchesConfig(repo, raw) {
				matched = true
			}
			if repoMatchesConfigProvenance(repo, raw) {
				provenanceRemains = true
			}
		}
		if !matched {
			continue
		}
		if !provenanceRemains {
			repo.ConfiguredRepoPath = ""
		}
		kept = append(kept, repo)
	}
	s.syncer.SetRepos(kept)
}

func repoMatchesOtherConfig(
	repo ghclient.RepoRef,
	target config.Repo,
	configured []config.Repo,
) bool {
	for _, raw := range configured {
		if sameConfiguredRepo(raw, target) {
			continue
		}
		if repoMatchesConfig(repo, raw) {
			return true
		}
	}
	return false
}

func sameConfiguredRepo(left, right config.Repo) bool {
	return strings.EqualFold(left.PlatformOrDefault(), right.PlatformOrDefault()) &&
		samePlatformHost(
			left.PlatformHostOrDefault(),
			right.PlatformHostOrDefault(),
		) &&
		strings.EqualFold(configRepoPath(left), configRepoPath(right))
}

func (s *Server) worktreeBasePathForRepo(
	ctx context.Context, provider, platformHost, owner, name string,
) (string, bool, error) {
	if s.cfg != nil {
		target := config.Repo{
			Platform:     provider,
			PlatformHost: platformHost,
			Owner:        owner,
			Name:         name,
		}
		configuredPath := func() string {
			s.cfgMu.Lock()
			defer s.cfgMu.Unlock()
			for _, repo := range s.cfg.Repos {
				if repo.HasNameGlob() || strings.TrimSpace(repo.WorktreeBasePath) == "" {
					continue
				}
				if sameConfiguredRepo(repo, target) {
					return repo.WorktreeBasePath
				}
			}
			return ""
		}()
		if configuredPath != "" {
			return configuredPath, true, nil
		}
	}
	if s.db == nil {
		return "", false, nil
	}
	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		return "", false, fmt.Errorf("list registered projects: %w", err)
	}
	var matchedPath string
	for _, project := range projects {
		identity := project.PlatformIdentity
		if project.IsStale || identity == nil ||
			!strings.EqualFold(identity.Platform, provider) ||
			!samePlatformHost(identity.Host, platformHost) ||
			!strings.EqualFold(identity.Owner, owner) ||
			!strings.EqualFold(identity.Name, name) {
			continue
		}
		if matchedPath != "" && matchedPath != project.LocalPath {
			return "", false, nil
		}
		matchedPath = project.LocalPath
	}
	if matchedPath != "" {
		return matchedPath, true, nil
	}
	return "", false, nil
}

func repoMatchesConfig(
	repo ghclient.RepoRef, raw config.Repo,
) bool {
	host := raw.PlatformHostOrDefault()
	if !strings.EqualFold(repoProvider(repo), raw.PlatformOrDefault()) ||
		!samePlatformHost(repo.PlatformHost, host) {
		return false
	}
	// A provider-side rename moves the tracked route (possibly across
	// owners) away from the configured path; provenance still ties the
	// repo to its exact entry.
	if repoConfiguredPathMatches(repo, raw) {
		return true
	}
	if !strings.EqualFold(repo.Owner, raw.Owner) {
		return false
	}
	if raw.HasNameGlob() {
		matched, _ := path.Match(
			strings.ToLower(raw.Name),
			strings.ToLower(repo.Name),
		)
		return matched
	}
	return strings.EqualFold(trackedRepoPath(repo), configRepoPath(raw)) ||
		strings.EqualFold(repo.Name, raw.Name)
}

// repoMatchesConfigProvenance reports whether raw is the exact entry the
// tracked repo's provenance names — provider- and host-scoped, since the
// same path can be configured on multiple providers or hosts.
func repoMatchesConfigProvenance(
	repo ghclient.RepoRef, raw config.Repo,
) bool {
	return strings.EqualFold(repoProvider(repo), raw.PlatformOrDefault()) &&
		samePlatformHost(repo.PlatformHost, raw.PlatformHostOrDefault()) &&
		repoConfiguredPathMatches(repo, raw)
}

func repoConfiguredPathMatches(
	repo ghclient.RepoRef, raw config.Repo,
) bool {
	return !raw.HasNameGlob() && repo.ConfiguredRepoPath != "" &&
		strings.EqualFold(repo.ConfiguredRepoPath, configRepoPath(raw))
}

func configRepoPath(raw config.Repo) string {
	if strings.TrimSpace(raw.RepoPath) != "" {
		return strings.TrimSpace(raw.RepoPath)
	}
	return raw.Owner + "/" + raw.Name
}

func trackedRepoPath(repo ghclient.RepoRef) string {
	if strings.TrimSpace(repo.RepoPath) != "" {
		return strings.TrimSpace(repo.RepoPath)
	}
	return repo.Owner + "/" + repo.Name
}

func repoProvider(repo ghclient.RepoRef) string {
	provider := string(repo.Platform)
	if provider == "" {
		return "github"
	}
	return strings.ToLower(provider)
}

func trackedRepoHost(repo ghclient.RepoRef) string {
	host := strings.TrimSpace(repo.PlatformHost)
	if host != "" {
		return strings.ToLower(host)
	}
	if defaultHost, ok := platform.DefaultHost(platform.Kind(repoProvider(repo))); ok {
		return defaultHost
	}
	return ""
}

func trackedRepoKey(repo ghclient.RepoRef) string {
	return repoProvider(repo) + "\x00" +
		trackedRepoHost(repo) + "\x00" +
		strings.ToLower(strings.Trim(trackedRepoPath(repo), "/ "))
}

// trackedRepoIdentityKey keys a tracked repo by its stable provider id, so a
// renamed route reconciles onto the same entry instead of tracking the
// repository twice. Empty when the ref carries no provider id.
// trackedProvenanceEntry records where a tracked ref's config-entry
// provenance came from, so route-keyed recovery can refuse to hand it to a
// different repository that merely reuses the route.
type trackedProvenanceEntry struct {
	path       string
	providerID string
}

// trackedRepoProvenance captures config-entry provenance from the tracked
// set before a settings merge rebuilds it. Settings-resolved refs never
// author provenance — only config resolution does — so a merge or glob
// refresh must not erase the correlation an exact entry needs to reclaim
// its repository on the next failed reload.
func trackedRepoProvenance(refs []ghclient.RepoRef) map[string]trackedProvenanceEntry {
	provenance := make(map[string]trackedProvenanceEntry)
	for _, repo := range refs {
		if repo.ConfiguredRepoPath == "" {
			continue
		}
		entry := trackedProvenanceEntry{
			path:       repo.ConfiguredRepoPath,
			providerID: strings.TrimSpace(repo.PlatformExternalID),
		}
		if key := trackedRepoIdentityKey(repo); key != "" {
			provenance["id\x00"+key] = entry
		}
		provenance["route\x00"+trackedRepoKey(repo)] = entry
	}
	return provenance
}

func withTrackedProvenance(
	provenance map[string]trackedProvenanceEntry, repo ghclient.RepoRef,
) ghclient.RepoRef {
	if repo.ConfiguredRepoPath != "" {
		return repo
	}
	if key := trackedRepoIdentityKey(repo); key != "" {
		if entry, ok := provenance["id\x00"+key]; ok {
			repo.ConfiguredRepoPath = entry.path
			return repo
		}
	}
	entry, ok := provenance["route\x00"+trackedRepoKey(repo)]
	if !ok {
		return repo
	}
	// A route match with two different stable provider ids is route reuse
	// by another repository, not a rename of the same one: provenance stays
	// with the identity it was resolved for. Provider ids are opaque and
	// case-sensitive — compared exactly, like identity keys.
	incomingID := strings.TrimSpace(repo.PlatformExternalID)
	if entry.providerID != "" && incomingID != "" &&
		entry.providerID != incomingID {
		return repo
	}
	repo.ConfiguredRepoPath = entry.path
	return repo
}

func trackedRepoIdentityKey(repo ghclient.RepoRef) string {
	if strings.TrimSpace(repo.PlatformExternalID) == "" {
		return ""
	}
	return repoProvider(repo) + "\x00" +
		trackedRepoHost(repo) + "\x00" + repo.PlatformExternalID
}

// trackedRepoIndex locates repo in current, matching by stable provider id
// first and falling back to the route key.
func trackedRepoIndex(
	byRoute, byIdentity map[string]int, repo ghclient.RepoRef,
) (int, bool) {
	if key := trackedRepoIdentityKey(repo); key != "" {
		if i, ok := byIdentity[key]; ok {
			return i, true
		}
	}
	i, ok := byRoute[trackedRepoKey(repo)]
	return i, ok
}

func indexTrackedRepo(
	byRoute, byIdentity map[string]int, repo ghclient.RepoRef, slot int,
) {
	byRoute[trackedRepoKey(repo)] = slot
	if key := trackedRepoIdentityKey(repo); key != "" {
		byIdentity[key] = slot
	}
}

func unindexTrackedRepo(
	byRoute, byIdentity map[string]int, repo ghclient.RepoRef,
) {
	delete(byRoute, trackedRepoKey(repo))
	if key := trackedRepoIdentityKey(repo); key != "" {
		delete(byIdentity, key)
	}
}

func (s *Server) persistResolvedRepos(
	ctx context.Context,
	repos []ghclient.RepoRef,
) error {
	for _, repo := range repos {
		if _, err := s.db.UpsertRepo(
			ctx, db.RepoIdentity{
				Platform:       repoProvider(repo),
				PlatformHost:   repo.PlatformHost,
				PlatformRepoID: repo.PlatformExternalID,
				Owner:          repo.Owner,
				Name:           repo.Name,
				RepoPath:       repo.RepoPath,
			},
		); err != nil {
			return fmt.Errorf(
				"upsert resolved repo %s/%s: %w",
				repo.Owner, repo.Name, err,
			)
		}
	}
	return nil
}

func samePlatformHost(left, right string) bool {
	if left == "" {
		left = "github.com"
	}
	if right == "" {
		right = "github.com"
	}
	return strings.EqualFold(left, right)
}

func (s *Server) defaultPlatformHost() string {
	if s.cfg == nil {
		return "github.com"
	}
	s.cfgMu.Lock()
	host := s.cfg.DefaultPlatformHost
	s.cfgMu.Unlock()
	if strings.TrimSpace(host) == "" {
		return "github.com"
	}
	return strings.ToLower(strings.TrimSpace(host))
}

// classifyResolveProblem maps a configured-repo resolve error to its wire
// problem through the shared provider mapping so a missing token during
// token-file rotation surfaces as 400 badRequest like the sync and runtime
// paths, not a 502 upstream error.
func classifyResolveProblem(err error) huma.StatusError {
	return httpapi.ProviderCallProblem(err, "github", "")
}

func (s *Server) getSettings(
	ctx context.Context, _ *struct{},
) (*getSettingsOutput, error) {
	if s.cfg == nil {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	return s.settingsOutputResponse(ctx)
}

// settingsOutputResponse wraps buildLocalSettingsResponse for handlers that
// answer with the full settings payload.
func (s *Server) settingsOutputResponse(
	ctx context.Context,
) (*settingsOutput, error) {
	body, err := s.buildLocalSettingsResponse(ctx)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	return &settingsOutput{Body: body}, nil
}

func (s *Server) updateSettings(
	ctx context.Context, input *updateSettingsInput,
) (*settingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	s.cfgMu.Lock()
	prevActivity := s.cfg.Activity
	prevPullRequests := s.cfg.PullRequests
	prevWorkspaces := s.cfg.Workspaces
	prevIssues := s.cfg.Issues
	prevTerminal := s.cfg.Terminal
	prevModes := cloneModeVisibility(s.cfg.Modes)
	prevAgents := cloneConfigAgents(s.cfg.Agents)
	prevKataProjects := slices.Clone(s.cfg.KataProjects)
	if input.Body.Activity != nil {
		candidate := *input.Body.Activity
		if candidate.ViewMode == "" {
			candidate.ViewMode = "threaded"
		}
		if candidate.TimeRange == "" {
			candidate.TimeRange = "7d"
		}
		s.cfg.Activity = candidate
	}
	if input.Body.PullRequests != nil {
		s.cfg.PullRequests = *input.Body.PullRequests
	}
	if input.Body.Workspaces != nil {
		s.cfg.Workspaces = *input.Body.Workspaces
	}
	if input.Body.Issues != nil {
		s.cfg.Issues = *input.Body.Issues
	}
	if input.Body.Terminal != nil {
		s.cfg.Terminal = *input.Body.Terminal
	}
	if input.Body.Modes != nil {
		s.cfg.Modes = cloneModeVisibility(*input.Body.Modes).WithDefaults()
	}
	if input.Body.Agents != nil {
		s.cfg.Agents = cloneConfigAgents(*input.Body.Agents)
	}
	if input.Body.KataProjects != nil {
		s.cfg.KataProjects = slices.Clone(*input.Body.KataProjects)
	}
	if err := s.cfg.Validate(); err != nil {
		s.cfg.Activity = prevActivity
		s.cfg.PullRequests = prevPullRequests
		s.cfg.Workspaces = prevWorkspaces
		s.cfg.Issues = prevIssues
		s.cfg.Terminal = prevTerminal
		s.cfg.Modes = prevModes
		s.cfg.Agents = prevAgents
		s.cfg.KataProjects = prevKataProjects
		s.cfgMu.Unlock()
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Activity = prevActivity
		s.cfg.PullRequests = prevPullRequests
		s.cfg.Workspaces = prevWorkspaces
		s.cfg.Issues = prevIssues
		s.cfg.Terminal = prevTerminal
		s.cfg.Modes = prevModes
		s.cfg.Agents = prevAgents
		s.cfg.KataProjects = prevKataProjects
		s.cfgMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	if s.syncer != nil {
		s.syncer.SetBranchActivityLimits(
			s.cfg.BranchActivityRetention(),
			s.cfg.Activity.DefaultBranchMaxCommits,
		)
	}
	nativeStacksEnabled := s.cfg.PullRequests.PreferGitHubNativeStacks
	nativeStacksPrevious := s.swapGitHubNativeStackPreferenceLocked(nativeStacksEnabled)
	s.refreshRuntimeTargetsLocked()
	s.applyWorkspaceConfigLocked()
	s.applyPullConfigLocked()
	s.cfgMu.Unlock()
	s.reconcileGitHubNativeStackProjection(nativeStacksPrevious, nativeStacksEnabled)

	return s.settingsOutputResponse(ctx)
}

func cloneModeVisibility(modes config.ModeVisibility) config.ModeVisibility {
	out := modes
	if modes.Activity != nil {
		v := *modes.Activity
		out.Activity = &v
	}
	if modes.Repos != nil {
		v := *modes.Repos
		out.Repos = &v
	}
	if modes.Docs != nil {
		v := *modes.Docs
		out.Docs = &v
	}
	if modes.Pulls != nil {
		v := *modes.Pulls
		out.Pulls = &v
	}
	if modes.Issues != nil {
		v := *modes.Issues
		out.Issues = &v
	}
	if modes.Reviews != nil {
		v := *modes.Reviews
		out.Reviews = &v
	}
	if modes.Workspaces != nil {
		v := *modes.Workspaces
		out.Workspaces = &v
	}
	return out
}

func cloneConfigAgents(agents []config.Agent) []config.Agent {
	if agents == nil {
		return []config.Agent{}
	}
	cloned := make([]config.Agent, len(agents))
	for i, agent := range agents {
		cloned[i] = agent
		cloned[i].Command = slices.Clone(agent.Command)
	}
	return cloned
}

func cloneFleetPeers(peers []config.FleetPeer) []config.FleetPeer {
	if peers == nil {
		return []config.FleetPeer{}
	}
	return slices.Clone(peers)
}

func cloneFleetSSHPeers(peers []config.FleetSSHPeer) []config.FleetSSHPeer {
	if peers == nil {
		return []config.FleetSSHPeer{}
	}
	return slices.Clone(peers)
}

func (s *Server) refreshRuntimeTargetsLocked() {
	if s.cfg == nil {
		return
	}
	if s.workspaces != nil {
		s.workspaces.SetHideTmuxStatus(s.cfg.Terminal.HideTmuxStatus)
	}
	if s.runtime == nil {
		return
	}
	tmuxCmd := s.bootTmuxCommand()
	targets := localruntime.ResolveLaunchTargets(s.cfg.Agents, tmuxCmd, nil)
	s.runtime.UpdateTargetsAndStripEnvVars(targets, s.cfg.TokenEnvNames())
	s.runtime.UpdateHideTmuxStatus(s.cfg.Terminal.HideTmuxStatus)
}

func (s *Server) bootTmuxCommand() []string {
	cfg := &config.Config{Tmux: s.bootCfgSnapshot.Tmux}
	return cfg.TmuxCommand()
}

func (s *Server) updateRuntimeStripEnvVars(cfg *config.Config) {
	if s.runtime == nil || cfg == nil {
		return
	}
	s.runtime.UpdateStripEnvVars(cfg.TokenEnvNames())
}

func (s *Server) addConfiguredRepo(
	ctx context.Context, input *addRepoInput,
) (*settingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}
	if input.Body.Owner == "" || input.Body.Name == "" {
		return nil, httpapi.Validation("body", "owner and name are required")
	}

	provider, err := normalizeRouteProvider(input.Body.Provider)
	if err != nil {
		return nil, httpapi.Validation("body.provider", err.Error())
	}
	newRepo := config.Repo{
		Platform:     provider,
		PlatformHost: importRequestHost(input.Body.Host, input.Body.PlatformHost),
		Owner:        input.Body.Owner,
		Name:         input.Body.Name,
	}

	// Pre-check (racy but gives a fast 400 before the GitHub call).
	s.cfgMu.Lock()
	for _, rp := range s.cfg.Repos {
		if sameConfiguredRepo(rp, newRepo) {
			s.cfgMu.Unlock()
			return nil, httpapi.BadRequest(httpapi.CodeBadRequest,
				input.Body.Owner+"/"+input.Body.Name+
					" is already configured", nil)
		}
	}
	allRepos := append(slices.Clone(s.cfg.Repos), newRepo)
	s.cfgMu.Unlock()

	_, expanded, err := ghclient.ResolveConfiguredRepo(
		ctx, s.configuredClients(allRepos), newRepo,
	)
	if err != nil {
		return nil, classifyResolveProblem(err)
	}

	// Re-acquire lock and apply the addition to current state
	// so concurrent activity/settings changes are not lost.
	s.cfgMu.Lock()
	for _, rp := range s.cfg.Repos {
		if sameConfiguredRepo(rp, newRepo) {
			s.cfgMu.Unlock()
			return nil, httpapi.BadRequest(httpapi.CodeBadRequest,
				input.Body.Owner+"/"+input.Body.Name+
					" is already configured", nil)
		}
	}
	s.cfg.Repos = append(s.cfg.Repos, newRepo)
	if err := s.cfg.Validate(); err != nil {
		s.cfg.Repos = s.cfg.Repos[:len(s.cfg.Repos)-1]
		s.cfgMu.Unlock()
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Repos = s.cfg.Repos[:len(s.cfg.Repos)-1]
		s.cfgMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	s.mergeTrackedRepos(expanded)
	s.applyWorkspaceConfigLocked()
	s.cfgMu.Unlock()

	s.syncer.TriggerRun(context.WithoutCancel(ctx))
	return s.settingsOutputResponse(ctx)
}

func (s *Server) refreshConfiguredRepo(
	ctx context.Context, input *repoConfigInput,
) (*settingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	owner := input.Owner
	name := input.Name
	provider, err := normalizeRouteProvider(input.Provider)
	if err != nil {
		return nil, httpapi.Validation("path.provider", err.Error())
	}
	targetRef := config.Repo{
		Platform:     provider,
		PlatformHost: input.PlatformHost,
		Owner:        owner,
		Name:         name,
	}

	s.cfgMu.Lock()
	repos := slices.Clone(s.cfg.Repos)
	s.cfgMu.Unlock()

	var target *config.Repo
	for i := range repos {
		if sameConfiguredRepo(
			repos[i],
			targetRef,
		) {
			target = &repos[i]
			break
		}
	}
	if target == nil {
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound,
			owner+"/"+name+" is not configured", nil)
	}
	if !target.HasNameGlob() {
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest,
			"refresh is only supported for glob patterns", nil)
	}

	_, expanded, err := ghclient.ResolveConfiguredRepo(
		ctx, s.configuredClients(repos), *target,
	)
	if err != nil {
		return nil, classifyResolveProblem(err)
	}

	// Re-acquire cfgMu and verify the target glob still exists
	// in the config before applying the resolved matches.
	// Without this, a concurrent DELETE on the same glob
	// could run between the unlock above and the helper below,
	// and the stale expansion would resurrect removed repos.
	s.cfgMu.Lock()
	stillExists := false
	currentRepos := slices.Clone(s.cfg.Repos)
	for _, rp := range currentRepos {
		if sameConfiguredRepo(
			rp,
			targetRef,
		) {
			stillExists = true
			break
		}
	}
	if !stillExists {
		s.cfgMu.Unlock()
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound,
			owner+"/"+name+" is no longer configured", nil)
	}
	if err := s.persistResolvedRepos(ctx, expanded); err != nil {
		s.cfgMu.Unlock()
		return nil, httpapi.Internal("persist resolved repos: " + err.Error())
	}
	s.replaceGlobRepos(*target, expanded, currentRepos)
	s.cfgMu.Unlock()

	s.syncer.TriggerRun(context.WithoutCancel(ctx))
	return s.settingsOutputResponse(ctx)
}

func (s *Server) refreshConfiguredRepoOnHost(
	ctx context.Context, input *repoConfigHostInput,
) (*settingsOutput, error) {
	return s.refreshConfiguredRepo(ctx, &repoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	})
}

func (s *Server) updateConfiguredRepoWorktreeBase(
	ctx context.Context, input *repoWorktreeBaseInput,
) (*settingsOutput, error) {
	return s.updateConfiguredRepoWorktreeBasePath(ctx, repoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}, input.Body.WorktreeBasePath)
}

func (s *Server) updateConfiguredRepoWorktreeBaseOnHost(
	ctx context.Context, input *repoWorktreeBaseHostInput,
) (*settingsOutput, error) {
	return s.updateConfiguredRepoWorktreeBasePath(ctx, repoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}, input.Body.WorktreeBasePath)
}

func (s *Server) updateConfiguredRepoWorktreeBasePath(
	ctx context.Context, ref repoConfigInput, rawPath string,
) (*settingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	provider, err := normalizeRouteProvider(ref.Provider)
	if err != nil {
		return nil, httpapi.Validation("path.provider", err.Error())
	}
	targetRef := config.Repo{
		Platform:     provider,
		PlatformHost: ref.PlatformHost,
		Owner:        ref.Owner,
		Name:         ref.Name,
	}

	worktreeBasePath := strings.TrimSpace(rawPath)
	if worktreeBasePath != "" {
		abs, err := workspace.ValidateWorktreeBasePath(
			ctx, worktreeBasePath, targetRef.PlatformHostOrDefault(),
			ref.Owner, ref.Name,
		)
		if err != nil {
			return nil, httpapi.Validation("body.worktree_base_path", err.Error())
		}
		worktreeBasePath = abs
	}

	s.cfgMu.Lock()
	idx := -1
	for i, rp := range s.cfg.Repos {
		if sameConfiguredRepo(rp, targetRef) {
			idx = i
			break
		}
	}
	if idx == -1 {
		s.cfgMu.Unlock()
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound,
			ref.Owner+"/"+ref.Name+" is not configured", nil)
	}
	if s.cfg.Repos[idx].HasNameGlob() {
		s.cfgMu.Unlock()
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest,
			"worktree base paths are only supported for exact repositories",
			nil,
		)
	}

	prev := s.cfg.Repos[idx].WorktreeBasePath
	s.cfg.Repos[idx].WorktreeBasePath = worktreeBasePath
	if err := s.cfg.Validate(); err != nil {
		s.cfg.Repos[idx].WorktreeBasePath = prev
		s.cfgMu.Unlock()
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Repos[idx].WorktreeBasePath = prev
		s.cfgMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	s.cfgMu.Unlock()

	return s.settingsOutputResponse(ctx)
}

func (s *Server) updateConfiguredRepoUIVisibility(
	ctx context.Context, input *repoUIVisibilityInput,
) (*settingsOutput, error) {
	return s.updateConfiguredRepoUIVisibilityState(ctx, repoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}, input.Body.Hidden)
}

func (s *Server) updateConfiguredRepoUIVisibilityOnHost(
	ctx context.Context, input *repoUIVisibilityHostInput,
) (*settingsOutput, error) {
	return s.updateConfiguredRepoUIVisibilityState(ctx, repoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}, input.Body.Hidden)
}

// updateConfiguredRepoUIVisibilityState persists the hidden-from-UI
// preference for the exact configured repository named by ref. The preference
// attaches to the catalog row's stable identity, so the entry must resolve to
// a provider-verified repository before it can be hidden.
func (s *Server) updateConfiguredRepoUIVisibilityState(
	ctx context.Context, ref repoConfigInput, hidden bool,
) (*settingsOutput, error) {
	if s.cfg == nil || s.db == nil {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	provider, err := normalizeRouteProvider(ref.Provider)
	if err != nil {
		return nil, httpapi.Validation("path.provider", err.Error())
	}
	targetRef := config.Repo{
		Platform:     provider,
		PlatformHost: ref.PlatformHost,
		Owner:        ref.Owner,
		Name:         ref.Name,
	}

	// Membership is validated and the preference written under the same
	// visibility lock the orphan sweep takes: a concurrent exact-entry
	// removal either completes first (the check below then rejects the
	// mutation) or waits for the write and sweeps it, so the preference can
	// never outlive its exact entry unreachable behind a glob.
	s.repoVisibilityMu.Lock()
	defer s.repoVisibilityMu.Unlock()

	s.cfgMu.Lock()
	var target *config.Repo
	for i := range s.cfg.Repos {
		if sameConfiguredRepo(s.cfg.Repos[i], targetRef) {
			raw := s.cfg.Repos[i]
			target = &raw
			break
		}
	}
	s.cfgMu.Unlock()
	if target == nil {
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound,
			ref.Owner+"/"+ref.Name+" is not configured", nil)
	}
	if target.HasNameGlob() {
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest,
			"UI visibility is only supported for exact repositories",
			nil,
		)
	}

	repo, err := s.applyVisibilityUnderReconciliationRead(
		ctx, s.visibilityLookupIdentity(*target), hidden,
	)
	if err != nil {
		return nil, httpapi.Internal("save visibility: " + err.Error())
	}
	if repo == nil {
		return nil, httpapi.Conflict(httpapi.CodeConflict,
			ref.Owner+"/"+ref.Name+
				" does not resolve to an active provider-verified repository yet; retry after sync",
			nil)
	}

	return s.settingsOutputResponse(ctx)
}

// applyVisibilityUnderReconciliationRead resolves the target catalog row and
// writes the preference in one critical section under the
// repository-reconciliation read lock, so reconciliation cannot displace the
// row between lifecycle validation and the write. Returns nil without error
// when no active provider-verified row resolves.
func (s *Server) applyVisibilityUnderReconciliationRead(
	ctx context.Context, identity db.RepoIdentity, hidden bool,
) (*db.Repo, error) {
	release, err := s.db.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	repo, err := s.resolveVisibilityRepoLocked(ctx, identity)
	if err != nil || repo == nil {
		return repo, err
	}
	if err := s.db.SetRepoHiddenFromUI(ctx, repo.ID, hidden); err != nil {
		return nil, err
	}
	return repo, nil
}

// resolveVisibilityRepoLocked resolves the catalog row a visibility mutation
// targets; the caller must hold the repository-reconciliation read lock. The
// stable provider id wins when the tracked ref carries one: route resolution
// would hand the mutation to whichever repository currently occupies the
// route, which after route reuse is a different repository. Inactive rows are
// rejected the same as unresolved ones — a tracked snapshot that lags
// reconciliation still names a displaced repository, and hiding it would
// leave the active replacement visible while consuming the request.
func (s *Server) resolveVisibilityRepoLocked(
	ctx context.Context, identity db.RepoIdentity,
) (*db.Repo, error) {
	if strings.TrimSpace(identity.PlatformRepoID) == "" {
		return s.db.GetRepoByIdentityUnderRepositoryReconciliationRead(ctx, identity)
	}
	entry, err := s.db.GetRepositoryByProviderIDUnderRepositoryReconciliationRead(
		ctx, identity.Platform, identity.PlatformHost, identity.PlatformRepoID,
	)
	if err != nil {
		return nil, err
	}
	if entry == nil || entry.Lifecycle != db.RepositoryLifecycleActive {
		return nil, nil
	}
	repo := entry.Repository
	return &repo, nil
}

// visibilityLookupIdentity names the catalog repository an exact configured
// entry currently resolves to. A tracked ref carries the provider-verified
// stable id and the current route after renames; without one (including on
// servers constructed without a syncer), the configured route itself is the
// only address.
func (s *Server) visibilityLookupIdentity(raw config.Repo) db.RepoIdentity {
	var tracked []ghclient.RepoRef
	if s.syncer != nil {
		tracked = s.syncer.TrackedRepos()
	}
	for _, repo := range tracked {
		if !repoMatchesConfig(repo, raw) {
			continue
		}
		return db.RepoIdentity{
			Platform:       repoProvider(repo),
			PlatformHost:   trackedRepoHost(repo),
			PlatformRepoID: repo.PlatformExternalID,
			Owner:          repo.Owner,
			Name:           repo.Name,
			RepoPath:       repo.RepoPath,
		}
	}
	return db.RepoIdentity{
		Platform:     raw.PlatformOrDefault(),
		PlatformHost: raw.PlatformHostOrDefault(),
		Owner:        raw.Owner,
		Name:         raw.Name,
		RepoPath:     raw.RepoPath,
	}
}

func (s *Server) deleteConfiguredRepo(
	ctx context.Context, input *repoConfigInput,
) (*struct{}, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	owner := input.Owner
	name := input.Name
	provider, err := normalizeRouteProvider(input.Provider)
	if err != nil {
		return nil, httpapi.Validation("path.provider", err.Error())
	}
	targetRef := config.Repo{
		Platform:     provider,
		PlatformHost: input.PlatformHost,
		Owner:        owner,
		Name:         name,
	}

	s.cfgMu.Lock()
	idx := -1
	for i, rp := range s.cfg.Repos {
		if sameConfiguredRepo(
			rp,
			targetRef,
		) {
			idx = i
			break
		}
	}
	if idx == -1 {
		s.cfgMu.Unlock()
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound,
			owner+"/"+name+" is not configured", nil)
	}

	prevRepos := slices.Clone(s.cfg.Repos)
	removed := prevRepos[idx]
	s.cfg.Repos = append(
		s.cfg.Repos[:idx], s.cfg.Repos[idx+1:]...,
	)
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Repos = prevRepos
		s.cfgMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	s.removeConfigRepos(s.cfg.Repos)
	s.applyWorkspaceConfigLocked()
	s.cfgMu.Unlock()

	// The hidden-from-UI preference belongs to an exact entry. Without one, a
	// glob can keep the repository tracked and filtered while glob rows expose
	// no visibility controls, so the preference would be unreachable. The
	// config change already committed and clients may abandon the request, so
	// the sweep runs detached from request cancellation; a failed sweep is
	// reported without failing the delete and heals on the next reload or
	// startup.
	if err := s.reconcileOrphanedRepoVisibility(
		context.WithoutCancel(ctx),
	); err != nil {
		slog.Warn("release hidden-from-UI preference on repo removal",
			"repo", configRepoPath(removed), "err", err)
	}

	return nil, nil
}

// reconcileOrphanedRepoVisibility clears every hidden-from-UI preference whose
// repository no longer resolves from an exact configured entry. It runs
// whenever the effective repository configuration changes: server startup,
// config hot reload, and exact-entry deletion. Inactive rows are accepted on
// the keep side and cleared like any other orphan: clearing a preference on a
// displaced row is safe and keeps it from lingering unreachable. Resolution
// errors abort the sweep without clearing anything.
func (s *Server) reconcileOrphanedRepoVisibility(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	s.repoVisibilityMu.Lock()
	defer s.repoVisibilityMu.Unlock()
	hidden, err := s.db.HiddenRepos(ctx)
	if err != nil {
		return err
	}
	if len(hidden) == 0 {
		return nil
	}
	var exact []config.Repo
	s.cfgMu.Lock()
	hasConfig := s.cfg != nil
	if hasConfig {
		for _, raw := range s.cfg.Repos {
			if raw.HasNameGlob() {
				continue
			}
			exact = append(exact, raw)
		}
	}
	s.cfgMu.Unlock()
	if !hasConfig {
		return nil
	}
	keep := make(map[int64]struct{}, len(exact))
	for _, raw := range exact {
		repo, err := s.lookupRepoForVisibilityRelease(
			ctx, s.visibilityLookupIdentity(raw),
		)
		if err != nil {
			return err
		}
		if repo != nil {
			keep[repo.ID] = struct{}{}
		}
	}
	for _, repo := range hidden {
		if _, kept := keep[repo.ID]; kept {
			continue
		}
		if err := s.db.SetRepoHiddenFromUI(ctx, repo.ID, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) lookupRepoForVisibilityRelease(
	ctx context.Context, identity db.RepoIdentity,
) (*db.Repo, error) {
	if strings.TrimSpace(identity.PlatformRepoID) == "" {
		return s.db.GetRepoByIdentity(ctx, identity)
	}
	entry, err := s.db.GetRepositoryByProviderID(
		ctx, identity.Platform, identity.PlatformHost, identity.PlatformRepoID,
	)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	repo := entry.Repository
	return &repo, nil
}

func normalizeRouteProvider(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("provider is required")
	}
	kind, err := platform.NormalizeKind(raw)
	if err != nil {
		return "", err
	}
	return string(kind), nil
}

func (s *Server) deleteConfiguredRepoOnHost(
	ctx context.Context, input *repoConfigHostInput,
) (*struct{}, error) {
	return s.deleteConfiguredRepo(ctx, &repoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	})
}
