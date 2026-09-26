package settingsapi

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
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
)

func (s *Handlers) ConfiguredClients(
	repos []config.Repo,
) map[string]ghclient.Client {
	clients := make(map[string]ghclient.Client)
	for _, repo := range repos {
		host := repo.PlatformHostOrDefault()
		if _, ok := clients[host]; ok {
			continue
		}
		client, err := (*s.Syncer).DirectClientForHost(host)
		if err != nil {
			continue
		}
		clients[host] = client
	}
	return clients
}

func (s *Handlers) ConfiguredRepoProjection(
	ctx context.Context,
	raw config.Repo,
	tracked []ghclient.RepoRef,
) (string, string, error) {
	if raw.HasNameGlob() {
		return "", "", nil
	}
	platformRepoID := strings.TrimSpace(raw.PlatformRepoID)
	if platformRepoID != "" {
		if s.Db != nil {
			entry, err := s.Db.GetRepositoryByProviderID(
				ctx, raw.PlatformOrDefault(), raw.PlatformHostOrDefault(), platformRepoID,
			)
			if err != nil {
				return "", "", fmt.Errorf(
					"resolve configured repo %s: %w", ConfigRepoPath(raw), err,
				)
			}
			if entry != nil && entry.Lifecycle == db.RepositoryLifecycleActive {
				return platformRepoID, entry.Repository.RepoPath, nil
			}
		}
		return platformRepoID, ConfigRepoPath(raw), nil
	}
	if s.Db != nil {
		entries, err := s.Db.ListRepositoryCatalog(ctx, db.RepositoryCatalogFilter{
			Platform: raw.PlatformOrDefault(), PlatformHost: raw.PlatformHostOrDefault(),
			RepoPath: ConfigRepoPath(raw),
		})
		if err != nil {
			return "", "", fmt.Errorf(
				"resolve configured repo %s: %w", ConfigRepoPath(raw), err,
			)
		}
		if len(entries) == 1 &&
			strings.TrimSpace(entries[0].Repository.PlatformRepoID) != "" {
			return entries[0].Repository.PlatformRepoID,
				entries[0].Repository.RepoPath, nil
		}
		if len(entries) > 1 {
			return "", "", nil
		}
	}
	return trackedPlatformRepoIDForConfig(raw, tracked),
		trackedPathForConfig(raw, tracked), nil
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
func (s *Handlers) HiddenRepoCorrelationSet(
	ctx context.Context,
) (hiddenRepoCorrelation, error) {
	if s.Db == nil {
		return hiddenRepoCorrelation{}, nil
	}
	hidden, err := s.Db.HiddenRepos(ctx)
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
func (s *Handlers) ConfigEntryHidden(
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
		ctx, s.VisibilityLookupIdentity(raw),
	)
	if err != nil {
		return false, fmt.Errorf(
			"resolve configured repo %s for hidden state: %w",
			ConfigRepoPath(raw), err,
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
			return spokeapi.TrackedRepoPath(repo)
		}
	}
	return ""
}

func trackedPlatformRepoIDForConfig(
	raw config.Repo, tracked []ghclient.RepoRef,
) string {
	if raw.HasNameGlob() {
		return ""
	}
	for _, repo := range tracked {
		if repoMatchesConfig(repo, raw) {
			return strings.TrimSpace(repo.PlatformExternalID)
		}
	}
	return ""
}

func MatchedRepoCount(
	raw config.Repo, tracked []ghclient.RepoRef,
) int {
	host := raw.PlatformHostOrDefault()
	provider := raw.PlatformOrDefault()
	count := 0
	for _, repo := range tracked {
		if !strings.EqualFold(spokeapi.RepoProvider(repo), provider) ||
			!spokeapi.SamePlatformHost(repo.PlatformHost, host) ||
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
		} else if strings.EqualFold(spokeapi.TrackedRepoPath(repo), ConfigRepoPath(raw)) ||
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
func (s *Handlers) MergeTrackedRepos(add []ghclient.RepoRef) {
	current := (*s.Syncer).TrackedRepos()
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
	(*s.Syncer).SetRepos(current)
}

// replaceGlobRepos removes repos that only match the refreshed
// glob entry, preserves repos still matched by other config
// entries, then adds the newly resolved matches.
func (s *Handlers) ReplaceGlobRepos(
	raw config.Repo,
	expanded []ghclient.RepoRef,
	configured []config.Repo,
) {
	current := (*s.Syncer).TrackedRepos()
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
	(*s.Syncer).SetRepos(kept)
}

// removeConfigRepos keeps only tracked repos that match at
// least one of the remaining config entries. A kept repo whose exact-entry
// provenance no longer names a remaining entry loses it: a stale claim
// would bind a future entry with the same path to the wrong repository.
func (s *Handlers) removeConfigRepos(
	remaining []config.Repo,
) {
	current := (*s.Syncer).TrackedRepos()
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
	(*s.Syncer).SetRepos(kept)
}

func repoMatchesOtherConfig(
	repo ghclient.RepoRef,
	target config.Repo,
	configured []config.Repo,
) bool {
	for _, raw := range configured {
		if SameConfiguredRepo(raw, target) {
			continue
		}
		if repoMatchesConfig(repo, raw) {
			return true
		}
	}
	return false
}

func SameConfiguredRepo(left, right config.Repo) bool {
	return strings.EqualFold(left.PlatformOrDefault(), right.PlatformOrDefault()) &&
		spokeapi.SamePlatformHost(
			left.PlatformHostOrDefault(),
			right.PlatformHostOrDefault(),
		) &&
		strings.EqualFold(ConfigRepoPath(left), ConfigRepoPath(right))
}

func (s *Handlers) WorktreeBasePathForRepo(
	ctx context.Context, repo workspace.WorktreeBaseRepository,
) (string, bool, error) {
	target := config.Repo{
		Platform: repo.Platform, PlatformHost: repo.PlatformHost,
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          repo.Owner, Name: repo.Name,
	}
	if (*s.Cfg) != nil {
		s.CfgMu.Lock()
		configuredRepos := slices.Clone((*s.Cfg).Repos)
		s.CfgMu.Unlock()
		targetID, _, err := s.ConfiguredRepoProjection(ctx, target, nil)
		if err != nil {
			return "", false, err
		}
		for _, repo := range configuredRepos {
			if repo.HasNameGlob() || strings.TrimSpace(repo.WorktreeBasePath) == "" {
				continue
			}
			repoID, _, err := s.ConfiguredRepoProjection(ctx, repo, nil)
			if err != nil {
				return "", false, err
			}
			stableMatch := targetID != "" && repoID == targetID &&
				strings.EqualFold(repo.PlatformOrDefault(), target.PlatformOrDefault()) &&
				spokeapi.SamePlatformHost(repo.PlatformHostOrDefault(), target.PlatformHostOrDefault())
			routeMatch := targetID == "" && SameConfiguredRepo(repo, target)
			if stableMatch || routeMatch {
				return repo.WorktreeBasePath, true, nil
			}
		}
	}
	if s.Db == nil {
		return "", false, nil
	}
	projects, err := s.Db.ListProjects(ctx)
	if err != nil {
		return "", false, fmt.Errorf("list registered projects: %w", err)
	}
	var matchedPath string
	for _, project := range projects {
		identity := project.PlatformIdentity
		if project.IsStale || identity == nil {
			continue
		}
		stableMatch := strings.TrimSpace(repo.PlatformRepoID) != "" &&
			strings.TrimSpace(identity.PlatformRepoID) == strings.TrimSpace(repo.PlatformRepoID)
		routeMatch := strings.TrimSpace(repo.PlatformRepoID) == "" &&
			strings.EqualFold(identity.Owner, repo.Owner) &&
			strings.EqualFold(identity.Name, repo.Name)
		if !strings.EqualFold(identity.Platform, repo.Platform) ||
			!spokeapi.SamePlatformHost(identity.Host, repo.PlatformHost) ||
			(!stableMatch && !routeMatch) {
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
	if !strings.EqualFold(spokeapi.RepoProvider(repo), raw.PlatformOrDefault()) ||
		!spokeapi.SamePlatformHost(repo.PlatformHost, host) {
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
	return strings.EqualFold(spokeapi.TrackedRepoPath(repo), ConfigRepoPath(raw)) ||
		strings.EqualFold(repo.Name, raw.Name)
}

// repoMatchesConfigProvenance reports whether raw is the exact entry the
// tracked repo's provenance names — provider- and host-scoped, since the
// same path can be configured on multiple providers or hosts.
func repoMatchesConfigProvenance(
	repo ghclient.RepoRef, raw config.Repo,
) bool {
	return strings.EqualFold(spokeapi.RepoProvider(repo), raw.PlatformOrDefault()) &&
		spokeapi.SamePlatformHost(repo.PlatformHost, raw.PlatformHostOrDefault()) &&
		repoConfiguredPathMatches(repo, raw)
}

func repoConfiguredPathMatches(
	repo ghclient.RepoRef, raw config.Repo,
) bool {
	return !raw.HasNameGlob() && repo.ConfiguredRepoPath != "" &&
		strings.EqualFold(repo.ConfiguredRepoPath, ConfigRepoPath(raw))
}

func ConfigRepoPath(raw config.Repo) string {
	if strings.TrimSpace(raw.RepoPath) != "" {
		return strings.TrimSpace(raw.RepoPath)
	}
	return raw.Owner + "/" + raw.Name
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
		provenance["route\x00"+spokeapi.TrackedRepoKey(repo)] = entry
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
	entry, ok := provenance["route\x00"+spokeapi.TrackedRepoKey(repo)]
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
	return spokeapi.RepoProvider(repo) + "\x00" +
		spokeapi.TrackedRepoHost(repo) + "\x00" + repo.PlatformExternalID
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
	i, ok := byRoute[spokeapi.TrackedRepoKey(repo)]
	return i, ok
}

func indexTrackedRepo(
	byRoute, byIdentity map[string]int, repo ghclient.RepoRef, slot int,
) {
	byRoute[spokeapi.TrackedRepoKey(repo)] = slot
	if key := trackedRepoIdentityKey(repo); key != "" {
		byIdentity[key] = slot
	}
}

func unindexTrackedRepo(
	byRoute, byIdentity map[string]int, repo ghclient.RepoRef,
) {
	delete(byRoute, spokeapi.TrackedRepoKey(repo))
	if key := trackedRepoIdentityKey(repo); key != "" {
		delete(byIdentity, key)
	}
}

func (s *Handlers) PersistResolvedRepos(
	ctx context.Context,
	repos []ghclient.RepoRef,
) error {
	for _, repo := range repos {
		if _, err := s.Db.UpsertRepo(
			ctx, db.RepoIdentity{
				Platform:       spokeapi.RepoProvider(repo),
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

func (s *Handlers) DefaultPlatformHost() string {
	if (*s.Cfg) == nil {
		return "github.com"
	}
	s.CfgMu.Lock()
	host := (*s.Cfg).DefaultPlatformHost
	s.CfgMu.Unlock()
	if strings.TrimSpace(host) == "" {
		return "github.com"
	}
	return strings.ToLower(strings.TrimSpace(host))
}

// classifyResolveProblem maps a configured-repo resolve error to its wire
// problem through the shared provider mapping so a missing token during
// token-file rotation surfaces as 400 badRequest like the sync and runtime
// paths, not a 502 upstream error.
func ClassifyResolveProblem(err error) huma.StatusError {
	return httpapi.ProviderCallProblem(err, "github", "")
}

func (s *Handlers) FetchProviderSettings(
	ctx context.Context,
) (*spokeapi.ProviderSettingsProjection, error) {
	if (*s.ProviderSource) == nil || (*s.ProviderSource).Client == nil ||
		((*s.ProviderSource).Enabled != nil && !(*s.ProviderSource).Enabled()) {
		return nil, nil
	}
	provider, err := (*s.ProviderSource).GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.ObserveProviderSettingsRepositories(
		ctx, provider.RepositoryObservations,
	); err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	return &provider, nil
}

func (s *Handlers) ObserveProviderSettingsRepositories(
	ctx context.Context,
	observations []spokeapi.ProviderRepositoryObservation,
) (bool, error) {
	if s.Db == nil {
		return false, nil
	}
	changed := false
	for _, observation := range observations {
		platformRepoID := strings.TrimSpace(observation.PlatformRepoID)
		if platformRepoID == "" || observation.ObservedAt.IsZero() {
			continue
		}
		repoPath := strings.Trim(strings.TrimSpace(observation.RepoPath), "/")
		owner, name := strings.TrimSpace(observation.Owner), strings.TrimSpace(observation.Name)
		current, err := s.Db.GetRepositoryByProviderID(
			ctx, observation.Provider, observation.PlatformHost, platformRepoID,
		)
		if err != nil {
			return false, fmt.Errorf("read provider settings repository: %w", err)
		}
		unchanged := current != nil && current.Lifecycle == db.RepositoryLifecycleActive &&
			strings.EqualFold(current.Repository.Owner, owner) &&
			strings.EqualFold(current.Repository.Name, name)
		_, accepted, err := s.Db.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
			Platform: observation.Provider, PlatformHost: observation.PlatformHost,
			PlatformRepoID: platformRepoID, Owner: owner, Name: name,
			RepoPath: repoPath,
		}, observation.ObservedAt)
		if err != nil {
			return false, fmt.Errorf("observe provider settings repository: %w", err)
		}
		changed = changed || (accepted && !unchanged)
	}
	return changed, nil
}

func SplitSettingsUpdate(
	update spokeapi.UpdateSettingsRequest,
) (provider spokeapi.UpdateSettingsRequest, local spokeapi.UpdateSettingsRequest) {
	provider.Activity = update.Activity
	provider.Detail = update.Detail
	provider.Sync = update.Sync
	provider.PullRequests = update.PullRequests
	provider.Issues = update.Issues
	local.AirplaneMode = update.AirplaneMode
	local.Workspaces = update.Workspaces
	local.Terminal = update.Terminal
	local.Modes = update.Modes
	local.Agents = update.Agents
	local.QuickActions = update.QuickActions
	local.KataProjects = update.KataProjects
	local.MCP = update.MCP
	local.Roborev = update.Roborev
	return provider, local
}

func HasSettingsUpdate(update spokeapi.UpdateSettingsRequest) bool {
	return update.AirplaneMode != nil || update.Activity != nil || update.Detail != nil ||
		update.Sync != nil ||
		update.PullRequests != nil || update.Workspaces != nil ||
		update.Issues != nil || update.Terminal != nil ||
		update.Modes != nil || update.Agents != nil ||
		update.QuickActions != nil ||
		update.KataProjects != nil || update.MCP != nil ||
		update.Roborev != nil
}

func CloneQuickActions(actions []config.QuickAction) []config.QuickAction {
	if actions == nil {
		return []config.QuickAction{}
	}
	return slices.Clone(actions)
}

func (s *Handlers) RefreshRuntimeTargetsLocked() {
	if (*s.Cfg) == nil {
		return
	}
	if (*s.Workspaces) != nil {
		(*s.Workspaces).SetHideTmuxStatus((*s.Cfg).Terminal.HideTmuxStatus)
		(*s.Workspaces).SetTmuxGraphics((*s.Cfg).TerminalGraphicsEnabled())
		(*s.Workspaces).SetTmuxMouse((*s.Cfg).TerminalTmuxMouseEnabled())
	}
	if (*s.Runtime) == nil {
		return
	}
	tmuxCmd := s.bootTmuxCommand()
	targets := localruntime.ResolveLaunchTargets((*s.Cfg).Agents, tmuxCmd, nil)
	(*s.Runtime).UpdateTargetsAndStripEnvVars(targets, (*s.Cfg).TokenEnvNames())
	(*s.Runtime).UpdateHideTmuxStatus((*s.Cfg).Terminal.HideTmuxStatus)
	(*s.Runtime).UpdateTmuxGraphics((*s.Cfg).TerminalGraphicsEnabled())
	(*s.Runtime).UpdateTmuxMouse((*s.Cfg).TerminalTmuxMouseEnabled())
}

func (s *Handlers) ApplyTmuxGraphics(ctx context.Context) {
	if (*s.Workspaces) == nil {
		return
	}
	if err := (*s.Workspaces).ApplyTmuxGraphics(ctx); err != nil {
		slog.Warn("apply tmux graphics setting", "err", err)
		return
	}
	if (*s.Runtime) != nil {
		if err := (*s.Runtime).ReattachTmuxClients(ctx); err != nil {
			slog.Warn("reattach tmux runtime clients", "err", err)
		}
	}
}

func (s *Handlers) ApplyTmuxMouse(ctx context.Context) {
	if (*s.Workspaces) == nil {
		return
	}
	if err := (*s.Workspaces).ApplyTmuxMouse(ctx); err != nil {
		slog.Warn("apply tmux mouse setting", "err", err)
	}
}

func (s *Handlers) bootTmuxCommand() []string {
	cfg := &config.Config{Tmux: s.BootCfgSnapshot.Tmux}
	return cfg.TmuxCommand()
}

func (s *Handlers) UpdateRuntimeStripEnvVars(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if (*s.Workspaces) != nil {
		(*s.Workspaces).UpdateTmuxStripEnvVars(cfg.TokenEnvNames())
	}
	if (*s.PtyOwnerClient) != nil {
		(*s.PtyOwnerClient).UpdateStripEnvVars(cfg.TokenEnvNames())
	}
	if (*s.Runtime) == nil {
		return
	}
	(*s.Runtime).UpdateStripEnvVars(cfg.TokenEnvNames())
}

func WorktreeBaseMutationTarget(
	target config.Repo, provider *spokeapi.ProviderSettingsProjection,
) (config.Repo, error) {
	if provider == nil {
		return target, nil
	}
	for _, candidate := range provider.Settings.Repos {
		if candidate.IsGlob ||
			!strings.EqualFold(candidate.Provider, target.PlatformOrDefault()) ||
			!spokeapi.SamePlatformHost(candidate.PlatformHost, target.PlatformHostOrDefault()) ||
			!strings.EqualFold(candidate.Owner, target.Owner) ||
			!strings.EqualFold(candidate.Name, target.Name) {
			continue
		}
		if strings.TrimSpace(candidate.PlatformRepoID) == "" {
			return config.Repo{}, spokeapi.InvalidHubDescriptor(
				errors.New("hub repository settings omitted stable identity"),
			)
		}
		return config.Repo{
			Platform: candidate.Provider, PlatformHost: candidate.PlatformHost,
			PlatformRepoID: candidate.PlatformRepoID,
			Owner:          candidate.Owner, Name: candidate.Name, RepoPath: candidate.RepoPath,
		}, nil
	}
	return config.Repo{}, httpapi.NotFound(
		httpapi.CodeRepoNotFound, target.Owner+"/"+target.Name+" is not configured", nil,
	)
}

func (s *Handlers) WorktreeBaseRepoIndexLocked(
	ctx context.Context, target config.Repo,
) (int, error) {
	for i, repo := range (*s.Cfg).Repos {
		if target.PlatformRepoID == "" {
			if SameConfiguredRepo(repo, target) {
				return i, nil
			}
			continue
		}
		platformRepoID := strings.TrimSpace(repo.PlatformRepoID)
		if platformRepoID == "" && !repo.HasNameGlob() {
			var err error
			platformRepoID, _, err = s.ConfiguredRepoProjection(ctx, repo, nil)
			if err != nil {
				return -1, err
			}
		}
		if platformRepoID == target.PlatformRepoID &&
			strings.EqualFold(repo.PlatformOrDefault(), target.PlatformOrDefault()) &&
			spokeapi.SamePlatformHost(repo.PlatformHostOrDefault(), target.PlatformHostOrDefault()) {
			return i, nil
		}
	}
	return -1, nil
}

// applyVisibilityUnderReconciliationRead resolves the target catalog row and
// writes the preference in one critical section under the
// repository-reconciliation read lock, so reconciliation cannot displace the
// row between lifecycle validation and the write. Returns nil without error
// when no active provider-verified row resolves.
func (s *Handlers) ApplyVisibilityUnderReconciliationRead(
	ctx context.Context, identity db.RepoIdentity, hidden bool,
) (*db.Repo, error) {
	release, err := s.Db.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	repo, err := s.resolveVisibilityRepoLocked(ctx, identity)
	if err != nil || repo == nil {
		return repo, err
	}
	if err := s.Db.SetRepoHiddenFromUI(ctx, repo.ID, hidden); err != nil {
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
func (s *Handlers) resolveVisibilityRepoLocked(
	ctx context.Context, identity db.RepoIdentity,
) (*db.Repo, error) {
	if strings.TrimSpace(identity.PlatformRepoID) == "" {
		return s.Db.GetRepoByIdentityUnderRepositoryReconciliationRead(ctx, identity)
	}
	entry, err := s.Db.GetRepositoryByProviderIDUnderRepositoryReconciliationRead(
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
func (s *Handlers) VisibilityLookupIdentity(raw config.Repo) db.RepoIdentity {
	var tracked []ghclient.RepoRef
	if (*s.Syncer) != nil {
		tracked = (*s.Syncer).TrackedRepos()
	}
	for _, repo := range tracked {
		if !repoMatchesConfig(repo, raw) {
			continue
		}
		return db.RepoIdentity{
			Platform:       spokeapi.RepoProvider(repo),
			PlatformHost:   spokeapi.TrackedRepoHost(repo),
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

func (s *Handlers) DeleteConfiguredRepo(
	ctx context.Context, input *RepoConfigInput,
) (*struct{}, error) {
	if (*s.CfgPath) == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	owner := input.Owner
	name := input.Name
	provider, err := NormalizeRouteProvider(input.Provider)
	if err != nil {
		return nil, httpapi.Validation("path.provider", err.Error())
	}
	targetRef := config.Repo{
		Platform:     provider,
		PlatformHost: input.PlatformHost,
		Owner:        owner,
		Name:         name,
	}

	s.ConfigReloadMu.Lock()
	s.CfgMu.Lock()
	idx := -1
	for i, rp := range (*s.Cfg).Repos {
		if SameConfiguredRepo(
			rp,
			targetRef,
		) {
			idx = i
			break
		}
	}
	if idx == -1 {
		s.CfgMu.Unlock()
		s.ConfigReloadMu.Unlock()
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound,
			owner+"/"+name+" is not configured", nil)
	}

	prevRepos := slices.Clone((*s.Cfg).Repos)
	removed := prevRepos[idx]
	(*s.Cfg).Repos = append(
		(*s.Cfg).Repos[:idx], (*s.Cfg).Repos[idx+1:]...,
	)
	if err := (*s.Cfg).Save((*s.CfgPath)); err != nil {
		(*s.Cfg).Repos = prevRepos
		s.CfgMu.Unlock()
		s.ConfigReloadMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	s.removeConfigRepos((*s.Cfg).Repos)
	s.ApplyWorkspaceConfigLocked()
	s.CfgMu.Unlock()
	s.ConfigReloadMu.Unlock()

	// The hidden-from-UI preference belongs to an exact entry. Without one, a
	// glob can keep the repository tracked and filtered while glob rows expose
	// no visibility controls, so the preference would be unreachable. The
	// config change already committed and clients may abandon the request, so
	// the sweep runs detached from request cancellation; a failed sweep is
	// reported without failing the delete and heals on the next reload or
	// startup.
	if err := s.ReconcileOrphanedRepoVisibility(
		context.WithoutCancel(ctx),
	); err != nil {
		slog.Warn("release hidden-from-UI preference on repo removal",
			"repo", ConfigRepoPath(removed), "err", err)
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
func (s *Handlers) ReconcileOrphanedRepoVisibility(ctx context.Context) error {
	if s.Db == nil {
		return nil
	}
	s.RepoVisibilityMu.Lock()
	defer s.RepoVisibilityMu.Unlock()
	hidden, err := s.Db.HiddenRepos(ctx)
	if err != nil {
		return err
	}
	if len(hidden) == 0 {
		return nil
	}
	var exact []config.Repo
	s.CfgMu.Lock()
	hasConfig := (*s.Cfg) != nil
	if hasConfig {
		for _, raw := range (*s.Cfg).Repos {
			if raw.HasNameGlob() {
				continue
			}
			exact = append(exact, raw)
		}
	}
	s.CfgMu.Unlock()
	if !hasConfig {
		return nil
	}
	keep := make(map[int64]struct{}, len(exact))
	for _, raw := range exact {
		repo, err := s.lookupRepoForVisibilityRelease(
			ctx, s.VisibilityLookupIdentity(raw),
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
		if err := s.Db.SetRepoHiddenFromUI(ctx, repo.ID, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *Handlers) lookupRepoForVisibilityRelease(
	ctx context.Context, identity db.RepoIdentity,
) (*db.Repo, error) {
	if strings.TrimSpace(identity.PlatformRepoID) == "" {
		return s.Db.GetRepoByIdentity(ctx, identity)
	}
	entry, err := s.Db.GetRepositoryByProviderID(
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

func NormalizeRouteProvider(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("provider is required")
	}
	kind, err := platform.NormalizeKind(raw)
	if err != nil {
		return "", err
	}
	return string(kind), nil
}

func (s *Handlers) DeleteConfiguredRepoOnHost(
	ctx context.Context, input *RepoConfigHostInput,
) (*struct{}, error) {
	return s.DeleteConfiguredRepo(ctx, &RepoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	})
}
