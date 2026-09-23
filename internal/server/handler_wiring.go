package server

import (
	"go.kenn.io/forge/internal/server/activityapi"
	"go.kenn.io/forge/internal/server/archiveapi"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/browserloginapi"
	"go.kenn.io/forge/internal/server/configreload"
	"go.kenn.io/forge/internal/server/devboxapi"
	"go.kenn.io/forge/internal/server/hostapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/notificationapi"
	"go.kenn.io/forge/internal/server/operationapi"
	"go.kenn.io/forge/internal/server/providerapi"
	"go.kenn.io/forge/internal/server/repoapi"
	"go.kenn.io/forge/internal/server/roborevapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/streamapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/server/telemetryapi"
)

// wireHandlers builds the Handlers of every package split out of this one.
// Mutable fields are shared by pointer so reloads and locks stay global.
func (s *Server) wireHandlers() {
	s.activityapi = &activityapi.Handlers{
		ActivityAfterItemsForTest: &s.activityAfterItemsForTest,
		Cfg:                       &s.cfg,
		CfgMu:                     &s.cfgMu,
		Clones:                    s.clones,
		Db:                        s.db,
		FleetAPI:                  &s.fleetAPI,
		IssueAPI:                  &s.issueAPI,
		Now:                       &s.now,
		ProviderSource:            &s.providerSource,
		PullAPI:                   &s.pullAPI,
		RepoResolver:              s.repoResolver,
		Syncer:                    &s.syncer,
		WorkspaceAPI:              &s.workspaceAPI,
	}
	s.archiveapi = &archiveapi.Handlers{
		Archive: s.archive,
		Syncer:  &s.syncer,
	}
	s.authapi = &authapi.Handlers{
		BasePath:            &s.basePath,
		Cfg:                 &s.cfg,
		DaemonRequests:      &s.daemonRequests,
		Db:                  s.db,
		HostOpts:            &s.hostOpts,
		Syncer:              &s.syncer,
		ViewerLoginCache:    &s.viewerLoginCache,
		ViewerLoginInFlight: &s.viewerLoginInFlight,
		ViewerLoginMu:       &s.viewerLoginMu,
	}
	s.browserloginapi = &browserloginapi.Handlers{
		BrowserLoginTickets: &s.browserLoginTickets,
	}
	s.configreload = &configreload.Handlers{
		BgCtx:               s.bgCtx,
		BootCfgSnapshot:     &s.bootCfgSnapshot,
		Cfg:                 &s.cfg,
		CfgMu:               &s.cfgMu,
		CfgPath:             &s.cfgPath,
		ConfigReloadMu:      &s.configReloadMu,
		ConfigWatcher:       &s.configWatcher,
		DocsAPI:             &s.docsAPI,
		Hub:                 &s.hub,
		PtyOwnerClient:      &s.ptyOwnerClient,
		Runtime:             &s.runtime,
		RuntimeStripEnvVars: &s.runtimeStripEnvVars,
		Syncer:              &s.syncer,
		TokenSources:        &s.tokenSources,
		Workspaces:          &s.workspaces,
	}
	s.devboxapi = &devboxapi.Handlers{
		Cfg:          &s.cfg,
		CfgMu:        &s.cfgMu,
		TokenSources: &s.tokenSources,
	}
	s.hostapi = &hostapi.Handlers{
		Cfg:     &s.cfg,
		Db:      s.db,
		Runtime: &s.runtime,
	}
	s.itemapi = &itemapi.Handlers{
		Db:                     s.db,
		LabelCatalogRefreshIDs: s.labelCatalogRefreshIDs,
		LabelCatalogRefreshMu:  &s.labelCatalogRefreshMu,
		PullAPI:                &s.pullAPI,
		RepoResolver:           s.repoResolver,
		Syncer:                 &s.syncer,
	}
	s.notificationapi = &notificationapi.Handlers{
		Cfg:    &s.cfg,
		Db:     s.db,
		Now:    &s.now,
		Syncer: &s.syncer,
	}
	s.operationapi = &operationapi.Handlers{
		BgCtx:                  s.bgCtx,
		Now:                    &s.now,
		RepoResolver:           s.repoResolver,
		WriteCredProbeInFlight: &s.writeCredProbeInFlight,
		WriteCredProbeMu:       &s.writeCredProbeMu,
		WriteCredProbes:        &s.writeCredProbes,
	}
	s.providerapi = &providerapi.Handlers{
		Db:                                      s.db,
		MarkdownImages:                          &s.markdownImages,
		Now:                                     &s.now,
		ProviderDescriptorBeforeSnapshotForTest: &s.providerDescriptorBeforeSnapshotForTest,
		RepoResolver:                            s.repoResolver,
		Syncer:                                  &s.syncer,
		WorkspaceAPI:                            &s.workspaceAPI,
	}
	s.repoapi = &repoapi.Handlers{
		Cfg:           &s.cfg,
		CfgMu:         &s.cfgMu,
		CfgPath:       &s.cfgPath,
		Db:            s.db,
		Now:           &s.now,
		RepoResolver:  s.repoResolver,
		Syncer:        &s.syncer,
		ToolingRun:    &s.toolingRun,
		ToolingStatus: &s.toolingStatus,
	}
	s.roborevapi = &roborevapi.Handlers{
		Cfg:                 &s.cfg,
		RoborevRepositories: &s.roborevRepositories,
	}
	s.routepolicy = &routepolicy.Handlers{
		Db: s.db,
		BuildVersion: func() string {
			return s.buildInfo.Version
		},
		BuildCommit: func() string {
			return s.buildInfo.Commit
		},
	}
	s.settingsapi = &settingsapi.Handlers{
		BootCfgSnapshot:  &s.bootCfgSnapshot,
		Cfg:              &s.cfg,
		CfgMu:            &s.cfgMu,
		CfgPath:          &s.cfgPath,
		ConfigReloadMu:   &s.configReloadMu,
		Db:               s.db,
		FleetAPI:         &s.fleetAPI,
		ProviderSource:   &s.providerSource,
		PtyOwnerClient:   &s.ptyOwnerClient,
		RepoVisibilityMu: &s.repoVisibilityMu,
		Runtime:          &s.runtime,
		Syncer:           &s.syncer,
		Workspaces:       &s.workspaces,
	}
	s.spokeapi = &spokeapi.Handlers{
		Clones: s.clones,
		Db:     s.db,
		Now:    &s.now,
	}
	s.streamapi = &streamapi.Handlers{
		AllowedHostMu:             &s.allowedHostMu,
		AllowedHosts:              &s.allowedHosts,
		BasePath:                  &s.basePath,
		Bg:                        &s.bg,
		BgCtx:                     s.bgCtx,
		BgMu:                      &s.bgMu,
		BootCfgSnapshot:           &s.bootCfgSnapshot,
		Cfg:                       &s.cfg,
		CfgMu:                     &s.cfgMu,
		ConnWG:                    &s.connWG,
		DaemonRequests:            &s.daemonRequests,
		Db:                        s.db,
		FleetAPI:                  &s.fleetAPI,
		FleetEnabledAtBoot:        &s.fleetEnabledAtBoot,
		HostOpts:                  &s.hostOpts,
		Hub:                       &s.hub,
		HubEvents:                 &s.hubEvents,
		IssueAPI:                  &s.issueAPI,
		KataAPI:                   &s.kataAPI,
		PtyOwnerClient:            &s.ptyOwnerClient,
		PullAPI:                   &s.pullAPI,
		Runtime:                   &s.runtime,
		ShuttingDown:              &s.shuttingDown,
		SpokeActivationLease:      &s.spokeActivationLease,
		TmuxCmd:                   &s.tmuxCmd,
		WorkspaceAPI:              &s.workspaceAPI,
		WorkspaceDependentsCancel: &s.workspaceDependentsCancel,
		WorkspaceDependentsCtx:    &s.workspaceDependentsCtx,
		WorkspaceDependentsDone:   s.workspaceDependentsDone,
		WorkspaceDependentsOnce:   &s.workspaceDependentsOnce,
		WorkspaceDependentsWG:     &s.workspaceDependentsWG,
		Workspaces:                &s.workspaces,
	}
	s.syncevents = &syncevents.Handlers{
		BgCtx:                 s.bgCtx,
		Db:                    s.db,
		DetailSyncInFlight:    &s.detailSyncInFlight,
		DetailSyncMu:          &s.detailSyncMu,
		DetailSyncPending:     &s.detailSyncPending,
		FederationStreams:     &s.federationStreams,
		FederationStreamsMu:   &s.federationStreamsMu,
		FederationStreamsNext: &s.federationStreamsNext,
		Hub:                   &s.hub,
		Now:                   &s.now,
		ProviderSource:        &s.providerSource,
		Syncer:                &s.syncer,
		WorkspaceAPI:          &s.workspaceAPI,
	}
	s.telemetryapi = &telemetryapi.Handlers{
		Telemetry: s.telemetry,
	}
	s.activityapi.DefaultPlatformHost = s.settingsapi.DefaultPlatformHost
	s.activityapi.EnqueueDetailSync = s.syncevents.EnqueueDetailSync
	s.activityapi.EnqueueDetailSyncOrRerun = s.syncevents.EnqueueDetailSyncOrRerun
	s.activityapi.FilterConfiguredRepoSummaries = s.repoapi.FilterConfiguredRepoSummaries
	s.activityapi.FilterConfiguredRepos = s.repoapi.FilterConfiguredRepos
	s.activityapi.FilterHiddenRepoSummaries = s.repoapi.FilterHiddenRepoSummaries
	s.activityapi.FilterHiddenRepos = s.repoapi.FilterHiddenRepos
	s.activityapi.NotificationsEnabled = s.notificationapi.NotificationsEnabled
	s.activityapi.RepoResponse = s.repoapi.RepoResponse
	s.activityapi.ResolveAuthenticatedViewerLogins = s.authapi.ResolveAuthenticatedViewerLogins
	s.activityapi.RunBackground = s.streamapi.RunBackground
	s.activityapi.ToRepoSummaryResponse = s.repoapi.ToRepoSummaryResponse
	s.activityapi.WorkspaceActivityRepositoryIdentities = s.providerapi.WorkspaceActivityRepositoryIdentities
	s.activityapi.WorkspaceActivityResponse = s.itemapi.WorkspaceActivityResponse
	s.archiveapi.RequireSync = s.activityapi.RequireSync
	s.authapi.FilterConfiguredRepos = s.repoapi.FilterConfiguredRepos
	s.configreload.ApplyFleetConfigLocked = s.streamapi.ApplyFleetConfigLocked
	s.configreload.ApplyIssueConfigLocked = s.streamapi.ApplyIssueConfigLocked
	s.configreload.ApplyKataConfigLocked = s.streamapi.ApplyKataConfigLocked
	s.configreload.ApplyPullConfigLocked = s.streamapi.ApplyPullConfigLocked
	s.configreload.ApplyTmuxGraphics = s.settingsapi.ApplyTmuxGraphics
	s.configreload.ApplyTmuxMouse = s.settingsapi.ApplyTmuxMouse
	s.configreload.ApplyWorkspaceConfigLocked = s.streamapi.ApplyWorkspaceConfigLocked
	s.configreload.ReconcileGitHubNativeStackProjection = s.syncevents.ReconcileGitHubNativeStackProjection
	s.configreload.ReconcileOrphanedRepoVisibility = s.settingsapi.ReconcileOrphanedRepoVisibility
	s.configreload.RefreshRuntimeTargetsLocked = s.settingsapi.RefreshRuntimeTargetsLocked
	s.configreload.RunBackground = s.streamapi.RunBackground
	s.configreload.SwapGitHubNativeStackPreferenceLocked = s.syncevents.SwapGitHubNativeStackPreferenceLocked
	s.configreload.UpdateCatalogStripEnvVars = s.streamapi.UpdateCatalogStripEnvVars
	s.configreload.UpdateRuntimeStripEnvVars = s.settingsapi.UpdateRuntimeStripEnvVars
	s.itemapi.IsConfiguredRepoTracked = s.repoapi.IsConfiguredRepoTracked
	s.itemapi.RunBackground = s.streamapi.RunBackground
	s.notificationapi.RunBackground = s.streamapi.RunBackground
	s.operationapi.MergeRequestAuthoredByViewer = s.MergeRequestAuthoredByViewer
	s.operationapi.MutationRateLimitedReason = s.MutationRateLimitedReason
	s.operationapi.OperationRateLimitBuckets = s.OperationRateLimitBuckets
	s.operationapi.WriteCredentialGateForRepo = s.WriteCredentialGateForRepo
	s.providerapi.EnqueueIssueSync = s.activityapi.EnqueueIssueSync
	s.providerapi.EnqueuePRSync = s.activityapi.EnqueuePRSync
	s.providerapi.GetCommentAutocomplete = s.itemapi.GetCommentAutocomplete
	s.providerapi.GetRepo = s.activityapi.GetRepo
	s.providerapi.GetRepoCommitDiff = s.activityapi.GetRepoCommitDiff
	s.providerapi.GetRepoCommitDiffOnHost = s.activityapi.GetRepoCommitDiffOnHost
	s.providerapi.ListRepoLabels = s.itemapi.ListRepoLabels
	s.providerapi.ResolveItem = s.itemapi.ResolveItem
	s.providerapi.SyncIssue = s.activityapi.SyncIssue
	s.providerapi.SyncPR = s.itemapi.SyncPR
	s.providerapi.SyncPRCI = s.activityapi.SyncPRCI
	s.repoapi.RepoOperations = s.operationapi.RepoOperations
	s.settingsapi.ActiveFleetConfigSnapshotLocked = s.streamapi.ActiveFleetConfigSnapshotLocked
	s.settingsapi.ApplyFleetConfigLocked = s.streamapi.ApplyFleetConfigLocked
	s.settingsapi.ApplyWorkspaceConfigLocked = s.streamapi.ApplyWorkspaceConfigLocked
	s.streamapi.ReconnectStaleEvent = s.syncevents.ReconnectStaleEvent
	s.syncevents.FederationEnabled = s.streamapi.FederationEnabled
	s.syncevents.RunBackground = s.streamapi.RunBackground
}

// wiredServer wires a hub built outside newServer, such as in tests.
func wiredServer(s *Server) *Server {
	s.wireHandlers()
	return s
}
