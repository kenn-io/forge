package server

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federationauth"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/configreload"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/repoapi"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
)

// buildLocalSettingsResponse builds the settings response from in-memory
// state (syncer tracked repos) plus the hidden-from-UI preferences persisted
// in SQLite, without calling the provider.
func (s *Server) buildLocalSettingsResponse(
	ctx context.Context,
) (spokeapi.SettingsResponse, error) {
	s.cfgMu.Lock()
	airplaneMode := s.cfg.AirplaneMode
	repos := slices.Clone(s.cfg.Repos)
	repoPresets := spokeapi.CloneRepoPresets(s.cfg.RepoPresets)
	if repoPresets == nil {
		repoPresets = []config.RepoPreset{}
	}
	activity := s.cfg.Activity
	detail := s.cfg.Detail
	syncSettings := spokeapi.SyncSettingsResponse{BudgetPerHour: s.cfg.BudgetPerHour()}
	pullRequests := s.cfg.PullRequests
	workspaces := s.cfg.Workspaces
	issues := s.cfg.Issues
	terminal := s.cfg.Terminal
	modes := spokeapi.CloneModeVisibility(s.cfg.Modes).WithDefaults()
	agents := spokeapi.CloneConfigAgents(s.cfg.Agents)
	quickActions := settingsapi.CloneQuickActions(s.cfg.QuickActions)
	kataProjects := slices.Clone(s.cfg.KataProjects)
	mcp := s.cfg.MCP
	roborev := s.cfg.Roborev
	if kataProjects == nil {
		// kata_projects is a required non-null array in the API schema, so a
		// nil clone (the default, no-mappings case) must serialize as [] rather
		// than null.
		kataProjects = []config.KataProjectRepoMapping{}
	}
	tmuxCommand := s.cfg.TmuxCommand()
	fleetSettings := s.settingsapi.BuildFleetSettingsResponseLocked()
	s.cfgMu.Unlock()
	launchTargets := localruntime.ResolveLaunchTargets(agents, tmuxCommand, nil)
	if launchTargets == nil {
		launchTargets = []localruntime.LaunchTarget{}
	}

	hiddenSet, err := s.settingsapi.HiddenRepoCorrelationSet(ctx)
	if err != nil {
		return spokeapi.SettingsResponse{}, err
	}
	var tracked []ghclient.RepoRef
	if s.syncer != nil {
		tracked = s.syncer.TrackedRepos()
	}
	configured := make(
		[]ghclient.ConfiguredRepoStatus, len(repos),
	)
	for i, raw := range repos {
		platformRepoID, trackedRepoPath, err := s.settingsapi.ConfiguredRepoProjection(
			ctx, raw, tracked,
		)
		if err != nil {
			return spokeapi.SettingsResponse{}, err
		}
		hiddenFromUI, err := s.settingsapi.ConfigEntryHidden(ctx, raw, tracked, hiddenSet)
		if err != nil {
			return spokeapi.SettingsResponse{}, err
		}
		caps := s.repoResolver.Capabilities(
			platform.Kind(raw.PlatformOrDefault()), raw.PlatformHostOrDefault(),
		)
		configured[i] = ghclient.ConfiguredRepoStatus{
			Provider:          raw.PlatformOrDefault(),
			PlatformHost:      raw.PlatformHostOrDefault(),
			PlatformRepoID:    platformRepoID,
			Owner:             raw.Owner,
			Name:              raw.Name,
			RepoPath:          settingsapi.ConfigRepoPath(raw),
			TrackedRepoPath:   trackedRepoPath,
			WorktreeBasePath:  raw.WorktreeBasePath,
			IsGlob:            raw.HasNameGlob(),
			MatchedRepoCount:  settingsapi.MatchedRepoCount(raw, tracked),
			HiddenFromUI:      hiddenFromUI,
			IssuePRReferences: caps.ReadIssuePRReferences,
		}
	}
	return spokeapi.SettingsResponse{
		AirplaneMode: airplaneMode,
		Repos:        configured,
		RepoPresets:  repoPresets,
		Activity:     activity,
		Detail:       detail,
		Sync:         syncSettings,
		PullRequests: pullRequests,
		Workspaces:   workspaces,
		Issues:       issues,
		// Notifications are a built-in capability with no enable/disable
		// setting; report them as always available.
		Notifications: spokeapi.NotificationsSettingsResponse{Enabled: true},
		Terminal:      terminal,
		Modes:         modes,
		Agents:        agents,
		QuickActions:  quickActions,
		KataProjects:  kataProjects,
		LaunchTargets: launchTargets,
		Fleet:         fleetSettings,
		MCP: spokeapi.McpSettingsResponse{
			Enabled:            mcp.Enabled,
			Port:               mcp.Port,
			DiffCacheMB:        mcp.DiffCacheMB,
			RestartRequired:    mcp != s.bootCfgSnapshot.MCP,
			ActiveURL:          s.options.MCPURL,
			ActiveRequiresAuth: s.bootCfgSnapshot.RequireAuth,
		},
		Roborev: spokeapi.RoborevSettingsResponse{
			InitManagedClones: roborev.InitManagedClones,
		},
	}, nil
}

func (s *Server) getSettings(
	ctx context.Context, _ *struct{},
) (*settingsapi.GetSettingsOutput, error) {
	if s.cfg == nil {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}
	if s.providerSource != nil && !s.streamapi.FederationEnabled() {
		return s.settingsOutputResponseWithProvider(ctx, nil)
	}

	return s.settingsOutputResponse(ctx)
}

func (s *Server) getLocalSettings(
	ctx context.Context, _ *struct{},
) (*settingsapi.SettingsOutput, error) {
	if s.cfg == nil {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}
	return s.settingsOutputResponseWithProvider(ctx, nil)
}

func (s *Server) mutateRepoPresets(
	ctx context.Context,
	mutate func([]config.RepoPreset) ([]config.RepoPreset, error),
) (*settingsapi.SettingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}
	s.configReloadMu.Lock()
	defer s.configReloadMu.Unlock()
	s.cfgMu.Lock()
	candidate := configreload.CloneReloadedConfig(s.cfg)
	next, err := mutate(spokeapi.CloneRepoPresets(candidate.RepoPresets))
	if err != nil {
		s.cfgMu.Unlock()
		return nil, err
	}
	candidate.RepoPresets = next
	if err := candidate.Validate(); err != nil {
		s.cfgMu.Unlock()
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := candidate.Save(s.cfgPath); err != nil {
		s.cfgMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	s.cfg.RepoPresets = spokeapi.CloneRepoPresets(candidate.RepoPresets)
	s.cfgMu.Unlock()
	return s.settingsOutputResponse(ctx)
}

func (s *Server) createRepoPreset(
	ctx context.Context, input *settingsapi.CreateRepoPresetInput,
) (*settingsapi.SettingsOutput, error) {
	return s.mutateRepoPresets(ctx, func(presets []config.RepoPreset) ([]config.RepoPreset, error) {
		for _, preset := range presets {
			if strings.EqualFold(preset.Name, input.Body.Name) {
				return nil, httpapi.Conflict(httpapi.CodeConflict, "repository preset already exists", nil)
			}
		}
		return append(presets, input.Body), nil
	})
}

func (s *Server) updateRepoPreset(
	ctx context.Context, input *settingsapi.UpdateRepoPresetInput,
) (*settingsapi.SettingsOutput, error) {
	return s.mutateRepoPresets(ctx, func(presets []config.RepoPreset) ([]config.RepoPreset, error) {
		for i := range presets {
			if strings.EqualFold(presets[i].Name, input.Name) {
				presets[i].Repos = slices.Clone(input.Body.Repos)
				return presets, nil
			}
		}
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "repository preset not found", nil)
	})
}

func (s *Server) deleteRepoPreset(
	ctx context.Context, input *settingsapi.DeleteRepoPresetInput,
) (*settingsapi.SettingsOutput, error) {
	return s.mutateRepoPresets(ctx, func(presets []config.RepoPreset) ([]config.RepoPreset, error) {
		for i := range presets {
			if strings.EqualFold(presets[i].Name, input.Name) {
				return append(presets[:i], presets[i+1:]...), nil
			}
		}
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "repository preset not found", nil)
	})
}

// settingsOutputResponse wraps buildLocalSettingsResponse for handlers that
// answer with the full settings payload.
func (s *Server) settingsOutputResponse(
	ctx context.Context,
) (*settingsapi.SettingsOutput, error) {
	provider, err := s.settingsapi.FetchProviderSettings(ctx)
	if err != nil {
		return nil, err
	}
	return s.settingsOutputResponseWithProvider(ctx, provider)
}

func (s *Server) settingsOutputResponseWithProvider(
	ctx context.Context, provider *spokeapi.ProviderSettingsProjection,
) (*settingsapi.SettingsOutput, error) {
	body, err := s.buildLocalSettingsResponse(ctx)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	if provider != nil {
		body.ApplyProviderSettings(provider.Settings)
	}
	body.ProviderSettingsLoaded = s.providerSource == nil || provider != nil
	return &settingsapi.SettingsOutput{Body: body}, nil
}

func (s *Server) updateSettings(
	ctx context.Context, input *settingsapi.UpdateSettingsInput,
) (*settingsapi.SettingsOutput, error) {
	if _, federationRequest := federationauth.PrincipalFromContext(ctx); federationRequest {
		providerUpdate, localUpdate := settingsapi.SplitSettingsUpdate(input.Body)
		if settingsapi.HasSettingsUpdate(localUpdate) {
			return nil, httpapi.Forbidden(
				"federation credentials cannot change hub-local settings",
				map[string]any{"reason": "nodeLocalSettings"},
			)
		}
		return s.updateLocalSettings(ctx, &settingsapi.UpdateSettingsInput{Body: providerUpdate})
	}
	if s.providerSource == nil {
		return s.updateLocalSettings(ctx, input)
	}
	providerUpdate, localUpdate := settingsapi.SplitSettingsUpdate(input.Body)
	providerChanged := settingsapi.HasSettingsUpdate(providerUpdate)
	localChanged := settingsapi.HasSettingsUpdate(localUpdate)
	if providerChanged && localChanged {
		return nil, httpapi.BadRequest(
			httpapi.CodeValidationError,
			"a spoke settings update cannot mix hub-owned and spoke-owned fields",
			map[string]any{"reason": "mixedSettingsOwnership"},
		)
	}
	if providerChanged {
		if _, err := s.providerSource.UpdateSettings(ctx, providerUpdate); err != nil {
			return nil, err
		}
	}
	if localChanged {
		return s.updateLocalSettings(ctx, &settingsapi.UpdateSettingsInput{Body: localUpdate})
	}
	return s.settingsOutputResponse(ctx)
}

func (s *Server) updateLocalSettings(
	ctx context.Context, input *settingsapi.UpdateSettingsInput,
) (*settingsapi.SettingsOutput, error) {
	if err := s.commitLocalSettings(ctx, input); err != nil {
		return nil, err
	}
	// A spoke's hub-owned fields are optional here: the local change is already
	// committed, so a slow or unavailable hub must not delay or fail the response.
	s.cfgMu.Lock()
	fleet := s.cfg.Fleet
	s.cfgMu.Unlock()
	providerContext, cancel := context.WithTimeout(ctx, fleet.PeerTimeoutOrDefault())
	defer cancel()
	provider, err := s.settingsapi.FetchProviderSettings(providerContext)
	if err != nil {
		slog.Warn("load hub settings after spoke-local settings save", "err", err)
		provider = nil
	}
	return s.settingsOutputResponseWithProvider(ctx, provider)
}

func (s *Server) commitLocalSettings(
	ctx context.Context, input *settingsapi.UpdateSettingsInput,
) error {
	if s.cfgPath == "" {
		return httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}
	if workspaces := input.Body.Workspaces; workspaces != nil && workspaces.DefaultExecutionTarget != nil {
		if err := config.ValidateDefaultExecutionTarget(*workspaces.DefaultExecutionTarget); err != nil {
			return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
		}
	}

	s.configReloadMu.Lock()
	defer s.configReloadMu.Unlock()
	s.cfgMu.Lock()
	prevAirplaneMode := s.cfg.AirplaneMode
	prevActivity := s.cfg.Activity
	prevDetail := s.cfg.Detail
	prevSyncBudgetPerHour := s.cfg.SyncBudgetPerHour
	prevPullRequests := s.cfg.PullRequests
	prevWorkspaces := s.cfg.Workspaces
	prevIssues := s.cfg.Issues
	prevTerminal := s.cfg.Terminal
	prevModes := spokeapi.CloneModeVisibility(s.cfg.Modes)
	prevAgents := spokeapi.CloneConfigAgents(s.cfg.Agents)
	prevQuickActions := settingsapi.CloneQuickActions(s.cfg.QuickActions)
	prevKataProjects := slices.Clone(s.cfg.KataProjects)
	prevMCP := s.cfg.MCP
	prevRoborev := s.cfg.Roborev
	if input.Body.AirplaneMode != nil {
		s.cfg.AirplaneMode = *input.Body.AirplaneMode
	}
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
	if input.Body.Detail != nil {
		s.cfg.Detail = *input.Body.Detail
	}
	if input.Body.Sync != nil && input.Body.Sync.BudgetPerHour != nil {
		s.cfg.SyncBudgetPerHour = *input.Body.Sync.BudgetPerHour
	}
	if input.Body.PullRequests != nil {
		s.cfg.PullRequests = *input.Body.PullRequests
	}
	if input.Body.Workspaces != nil {
		if input.Body.Workspaces.DefaultExecutionTarget != nil {
			s.cfg.Workspaces.DefaultExecutionTarget = *input.Body.Workspaces.DefaultExecutionTarget
		}
		if input.Body.Workspaces.AutoAssignOnCreate != nil {
			s.cfg.Workspaces.AutoAssignOnCreate = *input.Body.Workspaces.AutoAssignOnCreate
		}
		if input.Body.Workspaces.ShowAgentStatusInLists != nil {
			s.cfg.Workspaces.ShowAgentStatusInLists = *input.Body.Workspaces.ShowAgentStatusInLists
		}
		if input.Body.Workspaces.DefaultSidebarView != nil {
			s.cfg.Workspaces.DefaultSidebarView = *input.Body.Workspaces.DefaultSidebarView
		}
	}
	if input.Body.Issues != nil {
		s.cfg.Issues = *input.Body.Issues
	}
	if input.Body.Terminal != nil {
		s.cfg.Terminal = *input.Body.Terminal
	}
	if input.Body.Modes != nil {
		s.cfg.Modes = spokeapi.CloneModeVisibility(*input.Body.Modes).WithDefaults()
	}
	if input.Body.Agents != nil {
		s.cfg.Agents = spokeapi.CloneConfigAgents(*input.Body.Agents)
	}
	if input.Body.QuickActions != nil {
		s.cfg.QuickActions = settingsapi.CloneQuickActions(*input.Body.QuickActions)
	}
	if input.Body.KataProjects != nil {
		s.cfg.KataProjects = slices.Clone(*input.Body.KataProjects)
	}
	if input.Body.MCP != nil {
		if input.Body.MCP.Enabled != nil {
			s.cfg.MCP.Enabled = *input.Body.MCP.Enabled
		}
		if input.Body.MCP.Port != nil {
			s.cfg.MCP.Port = *input.Body.MCP.Port
		}
		if input.Body.MCP.DiffCacheMB != nil {
			s.cfg.MCP.DiffCacheMB = *input.Body.MCP.DiffCacheMB
		}
	}
	if input.Body.Roborev != nil && input.Body.Roborev.InitManagedClones != nil {
		s.cfg.Roborev.InitManagedClones = *input.Body.Roborev.InitManagedClones
	}
	if err := s.cfg.Validate(); err != nil {
		s.cfg.AirplaneMode = prevAirplaneMode
		s.cfg.Activity = prevActivity
		s.cfg.Detail = prevDetail
		s.cfg.SyncBudgetPerHour = prevSyncBudgetPerHour
		s.cfg.PullRequests = prevPullRequests
		s.cfg.Workspaces = prevWorkspaces
		s.cfg.Issues = prevIssues
		s.cfg.Terminal = prevTerminal
		s.cfg.Modes = prevModes
		s.cfg.Agents = prevAgents
		s.cfg.QuickActions = prevQuickActions
		s.cfg.KataProjects = prevKataProjects
		s.cfg.MCP = prevMCP
		s.cfg.Roborev = prevRoborev
		s.cfgMu.Unlock()
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.AirplaneMode = prevAirplaneMode
		s.cfg.Activity = prevActivity
		s.cfg.Detail = prevDetail
		s.cfg.SyncBudgetPerHour = prevSyncBudgetPerHour
		s.cfg.PullRequests = prevPullRequests
		s.cfg.Workspaces = prevWorkspaces
		s.cfg.Issues = prevIssues
		s.cfg.Terminal = prevTerminal
		s.cfg.Modes = prevModes
		s.cfg.Agents = prevAgents
		s.cfg.QuickActions = prevQuickActions
		s.cfg.KataProjects = prevKataProjects
		s.cfg.MCP = prevMCP
		s.cfg.Roborev = prevRoborev
		s.cfgMu.Unlock()
		return httpapi.Internal("save config: " + err.Error())
	}
	budgetRaised := s.cfg.SyncBudgetPerHour > prevSyncBudgetPerHour
	if s.syncer != nil {
		s.syncer.SetAirplaneMode(s.cfg.AirplaneMode)
		s.syncer.SetBudgetLimit(s.cfg.BudgetPerHour())
		s.syncer.SetBranchActivityLimits(
			s.cfg.BranchActivityRetention(),
			s.cfg.Activity.DefaultBranchMaxCommits,
		)
	}
	nativeStacksEnabled := s.cfg.PullRequests.PreferGitHubNativeStacks
	nativeStacksPrevious := s.syncevents.SwapGitHubNativeStackPreferenceLocked(nativeStacksEnabled)
	s.settingsapi.RefreshRuntimeTargetsLocked()
	s.streamapi.ApplyWorkspaceConfigLocked()
	s.streamapi.ApplyPullConfigLocked()
	s.streamapi.ApplyIssueConfigLocked()
	tmuxGraphicsChanged := (prevTerminal.Graphics == nil || *prevTerminal.Graphics) !=
		s.cfg.TerminalGraphicsEnabled()
	s.cfgMu.Unlock()
	if tmuxGraphicsChanged {
		s.settingsapi.ApplyTmuxGraphics(ctx)
	}
	s.settingsapi.ApplyTmuxMouse(ctx)
	s.syncevents.ReconcileGitHubNativeStackProjection(nativeStacksPrevious, nativeStacksEnabled)
	if budgetRaised && s.syncer != nil {
		// A sync paused at the old ceiling reports that failure until its
		// next pass; run one now so the raised ceiling takes visible effect.
		s.syncer.TriggerRun(context.WithoutCancel(ctx))
	}

	return nil
}

func (s *Server) addConfiguredRepo(
	ctx context.Context, input *settingsapi.AddRepoInput,
) (*settingsapi.SettingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}
	if input.Body.Owner == "" || input.Body.Name == "" {
		return nil, httpapi.Validation("body", "owner and name are required")
	}

	provider, err := settingsapi.NormalizeRouteProvider(input.Body.Provider)
	if err != nil {
		return nil, httpapi.Validation("body.provider", err.Error())
	}
	newRepo := config.Repo{
		Platform:     provider,
		PlatformHost: repoapi.ImportRequestHost(input.Body.Host, input.Body.PlatformHost),
		Owner:        input.Body.Owner,
		Name:         input.Body.Name,
	}

	// Pre-check (racy but gives a fast 400 before the GitHub call).
	s.cfgMu.Lock()
	for _, rp := range s.cfg.Repos {
		if settingsapi.SameConfiguredRepo(rp, newRepo) {
			s.cfgMu.Unlock()
			return nil, httpapi.BadRequest(httpapi.CodeBadRequest,
				input.Body.Owner+"/"+input.Body.Name+
					" is already configured", nil)
		}
	}
	allRepos := append(slices.Clone(s.cfg.Repos), newRepo)
	s.cfgMu.Unlock()

	_, expanded, err := ghclient.ResolveConfiguredRepo(
		ctx, s.settingsapi.ConfiguredClients(allRepos), newRepo,
	)
	if err != nil {
		return nil, settingsapi.ClassifyResolveProblem(err)
	}

	// Re-acquire lock and apply the addition to current state
	// so concurrent activity/settings changes are not lost.
	s.configReloadMu.Lock()
	s.cfgMu.Lock()
	for _, rp := range s.cfg.Repos {
		if settingsapi.SameConfiguredRepo(rp, newRepo) {
			s.cfgMu.Unlock()
			s.configReloadMu.Unlock()
			return nil, httpapi.BadRequest(httpapi.CodeBadRequest,
				input.Body.Owner+"/"+input.Body.Name+
					" is already configured", nil)
		}
	}
	s.cfg.Repos = append(s.cfg.Repos, newRepo)
	if err := s.cfg.Validate(); err != nil {
		s.cfg.Repos = s.cfg.Repos[:len(s.cfg.Repos)-1]
		s.cfgMu.Unlock()
		s.configReloadMu.Unlock()
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Repos = s.cfg.Repos[:len(s.cfg.Repos)-1]
		s.cfgMu.Unlock()
		s.configReloadMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	s.settingsapi.MergeTrackedRepos(expanded)
	s.streamapi.ApplyWorkspaceConfigLocked()
	s.cfgMu.Unlock()
	s.configReloadMu.Unlock()

	s.syncer.TriggerRun(context.WithoutCancel(ctx))
	return s.settingsOutputResponse(ctx)
}

func (s *Server) refreshConfiguredRepo(
	ctx context.Context, input *settingsapi.RepoConfigInput,
) (*settingsapi.SettingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	owner := input.Owner
	name := input.Name
	provider, err := settingsapi.NormalizeRouteProvider(input.Provider)
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
		if settingsapi.SameConfiguredRepo(
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

	_, expanded, err := s.syncer.ResolveConfiguredRepoForSync(ctx, *target)
	if err != nil {
		return nil, settingsapi.ClassifyResolveProblem(err)
	}

	// Re-acquire cfgMu and verify the target glob still exists
	// in the config before applying the resolved matches.
	// Without this, a concurrent DELETE on the same glob
	// could run between the unlock above and the helper below,
	// and the stale expansion would resurrect removed repos.
	s.configReloadMu.Lock()
	s.cfgMu.Lock()
	stillExists := false
	currentRepos := slices.Clone(s.cfg.Repos)
	for _, rp := range currentRepos {
		if settingsapi.SameConfiguredRepo(
			rp,
			targetRef,
		) {
			stillExists = true
			break
		}
	}
	if !stillExists {
		s.cfgMu.Unlock()
		s.configReloadMu.Unlock()
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound,
			owner+"/"+name+" is no longer configured", nil)
	}
	if err := s.settingsapi.PersistResolvedRepos(ctx, expanded); err != nil {
		s.cfgMu.Unlock()
		s.configReloadMu.Unlock()
		return nil, httpapi.Internal("persist resolved repos: " + err.Error())
	}
	s.settingsapi.ReplaceGlobRepos(*target, expanded, currentRepos)
	s.cfgMu.Unlock()
	s.configReloadMu.Unlock()

	s.syncer.TriggerRun(context.WithoutCancel(ctx))
	return s.settingsOutputResponse(ctx)
}

func (s *Server) refreshConfiguredRepoOnHost(
	ctx context.Context, input *settingsapi.RepoConfigHostInput,
) (*settingsapi.SettingsOutput, error) {
	return s.refreshConfiguredRepo(ctx, &settingsapi.RepoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	})
}

func (s *Server) updateConfiguredRepoWorktreeBase(
	ctx context.Context, input *settingsapi.RepoWorktreeBaseInput,
) (*settingsapi.SettingsOutput, error) {
	return s.updateConfiguredRepoWorktreeBasePath(ctx, settingsapi.RepoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}, input.Body.WorktreeBasePath)
}

func (s *Server) updateConfiguredRepoWorktreeBaseOnHost(
	ctx context.Context, input *settingsapi.RepoWorktreeBaseHostInput,
) (*settingsapi.SettingsOutput, error) {
	return s.updateConfiguredRepoWorktreeBasePath(ctx, settingsapi.RepoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}, input.Body.WorktreeBasePath)
}

func (s *Server) updateConfiguredRepoWorktreeBasePath(
	ctx context.Context, ref settingsapi.RepoConfigInput, rawPath string,
) (*settingsapi.SettingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	provider, err := settingsapi.NormalizeRouteProvider(ref.Provider)
	if err != nil {
		return nil, httpapi.Validation("path.provider", err.Error())
	}
	targetRef := config.Repo{
		Platform:     provider,
		PlatformHost: ref.PlatformHost,
		Owner:        ref.Owner,
		Name:         ref.Name,
	}
	providerSettings, err := s.settingsapi.FetchProviderSettings(ctx)
	if err != nil {
		return nil, err
	}
	targetRef, err = settingsapi.WorktreeBaseMutationTarget(targetRef, providerSettings)
	if err != nil {
		return nil, err
	}

	worktreeBasePath := strings.TrimSpace(rawPath)
	if worktreeBasePath != "" {
		allowInsecureHTTP := s.clones != nil && s.clones.AllowsInsecureHTTP(
			provider, targetRef.PlatformHostOrDefault(),
		)
		base, err := workspace.ValidateWorktreeBasePath(
			ctx, worktreeBasePath, targetRef.PlatformHostOrDefault(),
			targetRef.Owner, targetRef.Name, allowInsecureHTTP,
		)
		if err != nil {
			return nil, httpapi.Validation("body.worktree_base_path", err.Error())
		}
		worktreeBasePath = base.Path
	}

	s.configReloadMu.Lock()
	defer s.configReloadMu.Unlock()
	s.cfgMu.Lock()
	idx, err := s.settingsapi.WorktreeBaseRepoIndexLocked(ctx, targetRef)
	if err != nil {
		s.cfgMu.Unlock()
		return nil, httpapi.Internal(err.Error())
	}
	appended := false
	if idx == -1 {
		if providerSettings == nil {
			s.cfgMu.Unlock()
			return nil, httpapi.NotFound(httpapi.CodeRepoNotFound,
				ref.Owner+"/"+ref.Name+" is not configured", nil)
		}
		s.cfg.Repos = append(s.cfg.Repos, targetRef)
		idx = len(s.cfg.Repos) - 1
		appended = true
	}
	if s.cfg.Repos[idx].HasNameGlob() {
		s.cfgMu.Unlock()
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest,
			"worktree base paths are only supported for exact repositories",
			nil,
		)
	}

	prev := s.cfg.Repos[idx]
	restore := func() {
		if appended {
			s.cfg.Repos = s.cfg.Repos[:idx]
			return
		}
		s.cfg.Repos[idx] = prev
	}
	s.cfg.Repos[idx].Platform = targetRef.Platform
	s.cfg.Repos[idx].PlatformHost = targetRef.PlatformHost
	s.cfg.Repos[idx].PlatformRepoID = targetRef.PlatformRepoID
	s.cfg.Repos[idx].Owner = targetRef.Owner
	s.cfg.Repos[idx].Name = targetRef.Name
	s.cfg.Repos[idx].RepoPath = targetRef.RepoPath
	s.cfg.Repos[idx].WorktreeBasePath = worktreeBasePath
	if err := s.cfg.Validate(); err != nil {
		restore()
		s.cfgMu.Unlock()
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		restore()
		s.cfgMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	s.cfgMu.Unlock()

	return s.settingsOutputResponseWithProvider(ctx, providerSettings)
}

func (s *Server) updateConfiguredRepoUIVisibility(
	ctx context.Context, input *settingsapi.RepoUIVisibilityInput,
) (*settingsapi.SettingsOutput, error) {
	return s.updateConfiguredRepoUIVisibilityState(ctx, settingsapi.RepoConfigInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}, input.Body.Hidden)
}

func (s *Server) updateConfiguredRepoUIVisibilityOnHost(
	ctx context.Context, input *settingsapi.RepoUIVisibilityHostInput,
) (*settingsapi.SettingsOutput, error) {
	return s.updateConfiguredRepoUIVisibilityState(ctx, settingsapi.RepoConfigInput{
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
	ctx context.Context, ref settingsapi.RepoConfigInput, hidden bool,
) (*settingsapi.SettingsOutput, error) {
	if s.cfg == nil || s.db == nil {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	provider, err := settingsapi.NormalizeRouteProvider(ref.Provider)
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
		if settingsapi.SameConfiguredRepo(s.cfg.Repos[i], targetRef) {
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

	repo, err := s.settingsapi.ApplyVisibilityUnderReconciliationRead(
		ctx, s.settingsapi.VisibilityLookupIdentity(*target), hidden,
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
