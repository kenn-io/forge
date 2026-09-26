package spokeapi

import (
	"slices"
	"strings"

	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
)

type SettingsResponse struct {
	AirplaneMode  bool                            `json:"airplane_mode"`
	Repos         []ghclient.ConfiguredRepoStatus `json:"repos" nullable:"false"`
	RepoPresets   []config.RepoPreset             `json:"repo_presets" nullable:"false"`
	Activity      config.Activity                 `json:"activity"`
	Detail        config.Detail                   `json:"detail"`
	Sync          SyncSettingsResponse            `json:"sync"`
	PullRequests  config.PullRequests             `json:"pull_requests"`
	Workspaces    config.Workspaces               `json:"workspaces"`
	Issues        config.Issues                   `json:"issues"`
	Notifications NotificationsSettingsResponse   `json:"notifications"`
	Terminal      config.Terminal                 `json:"terminal"`
	Modes         config.ModeVisibility           `json:"modes,omitzero"`
	Agents        []config.Agent                  `json:"agents" nullable:"false"`
	QuickActions  []config.QuickAction            `json:"quick_actions" nullable:"false"`
	KataProjects  []config.KataProjectRepoMapping `json:"kata_projects" nullable:"false"`
	LaunchTargets []localruntime.LaunchTarget     `json:"launch_targets,omitempty"`
	Fleet         FleetSettingsResponse           `json:"fleet"`
	MCP           McpSettingsResponse             `json:"mcp"`
	Roborev       RoborevSettingsResponse         `json:"roborev"`
	// ProviderSettingsLoaded is false on a spoke whose response lacks the hub's
	// settings; its hub-owned fields then hold spoke-local values.
	ProviderSettingsLoaded bool `json:"provider_settings_loaded" doc:"Whether hub-owned fields (repositories, presets, activity, detail, sync, pull requests, issues, notifications) hold the effective values. False on a spoke when the hub's settings were not loaded; those fields cannot be edited until they are."`
}

// syncSettingsResponse reports the effective hourly sync ceiling. The schema
// bounds mirror config.MinSyncBudgetPerHour and config.MaxSyncBudgetPerHour so
// the UI can reject an out-of-range value before sending it.
type SyncSettingsResponse struct {
	BudgetPerHour int `json:"budget_per_hour" minimum:"50" maximum:"15000"`
}

type NotificationsSettingsResponse struct {
	Enabled bool `json:"enabled"`
}

type McpSettingsResponse struct {
	Enabled            bool   `json:"enabled"`
	Port               int    `json:"port,omitempty"`
	DiffCacheMB        int    `json:"diff_cache_mb,omitempty"`
	RestartRequired    bool   `json:"restart_required"`
	ActiveURL          string `json:"active_url,omitempty"`
	ActiveRequiresAuth bool   `json:"active_requires_auth"`
}

type RoborevSettingsResponse struct {
	InitManagedClones bool `json:"init_managed_clones"`
}

type UpdateSettingsRequest struct {
	AirplaneMode *bool                            `json:"airplane_mode,omitempty"`
	Activity     *config.Activity                 `json:"activity,omitempty"`
	Detail       *config.Detail                   `json:"detail,omitempty"`
	Sync         *SyncSettingsUpdate              `json:"sync,omitempty"`
	PullRequests *config.PullRequests             `json:"pull_requests,omitempty"`
	Workspaces   *WorkspaceSettingsUpdate         `json:"workspaces,omitempty"`
	Issues       *config.Issues                   `json:"issues,omitempty"`
	Terminal     *config.Terminal                 `json:"terminal,omitempty"`
	Modes        *config.ModeVisibility           `json:"modes,omitempty"`
	Agents       *[]config.Agent                  `json:"agents,omitempty"`
	QuickActions *[]config.QuickAction            `json:"quick_actions,omitempty"`
	KataProjects *[]config.KataProjectRepoMapping `json:"kata_projects,omitempty"`
	MCP          *McpSettingsUpdate               `json:"mcp,omitempty"`
	Roborev      *RoborevSettingsUpdate           `json:"roborev,omitempty"`
}

type WorkspaceSettingsUpdate struct {
	DefaultExecutionTarget *string `json:"default_execution_target,omitempty"`
	ShowAgentStatusInLists *bool   `json:"show_agent_status_in_lists,omitempty"`
	AutoAssignOnCreate     *bool   `json:"auto_assign_on_create,omitempty"`
	DefaultSidebarView     *string `json:"default_sidebar_view,omitempty" enum:"diff,item"`
}

type SyncSettingsUpdate struct {
	BudgetPerHour *int `json:"budget_per_hour,omitempty" minimum:"50" maximum:"15000"`
}

type McpSettingsUpdate struct {
	Enabled     *bool `json:"enabled,omitempty"`
	Port        *int  `json:"port,omitempty"`
	DiffCacheMB *int  `json:"diff_cache_mb,omitempty"`
}

type RoborevSettingsUpdate struct {
	InitManagedClones *bool `json:"init_managed_clones,omitempty"`
}

func TrackedRepoPath(repo ghclient.RepoRef) string {
	if strings.TrimSpace(repo.RepoPath) != "" {
		return strings.TrimSpace(repo.RepoPath)
	}
	return repo.Owner + "/" + repo.Name
}

func RepoProvider(repo ghclient.RepoRef) string {
	provider := string(repo.Platform)
	if provider == "" {
		return "github"
	}
	return strings.ToLower(provider)
}

func TrackedRepoHost(repo ghclient.RepoRef) string {
	host := strings.TrimSpace(repo.PlatformHost)
	if host != "" {
		return strings.ToLower(host)
	}
	if defaultHost, ok := platform.DefaultHost(platform.Kind(RepoProvider(repo))); ok {
		return defaultHost
	}
	return ""
}

func TrackedRepoKey(repo ghclient.RepoRef) string {
	return RepoProvider(repo) + "\x00" +
		TrackedRepoHost(repo) + "\x00" +
		strings.ToLower(strings.Trim(TrackedRepoPath(repo), "/ "))
}

func SamePlatformHost(left, right string) bool {
	if left == "" {
		left = "github.com"
	}
	if right == "" {
		right = "github.com"
	}
	return strings.EqualFold(left, right)
}

func (s *SettingsResponse) ApplyProviderSettings(provider SettingsResponse) {
	localRepos := s.Repos
	s.Repos = provider.Repos
	for i := range s.Repos {
		for _, local := range localRepos {
			if s.Repos[i].PlatformRepoID != "" &&
				local.PlatformRepoID != "" &&
				s.Repos[i].PlatformRepoID == local.PlatformRepoID &&
				strings.EqualFold(s.Repos[i].Provider, local.Provider) &&
				SamePlatformHost(s.Repos[i].PlatformHost, local.PlatformHost) {
				s.Repos[i].WorktreeBasePath = local.WorktreeBasePath
				break
			}
		}
	}
	s.RepoPresets = provider.RepoPresets
	s.Activity = provider.Activity
	s.Detail = provider.Detail
	s.Sync = provider.Sync
	s.PullRequests = provider.PullRequests
	s.Issues = provider.Issues
	s.Notifications = provider.Notifications
}

func CloneRepoPresets(presets []config.RepoPreset) []config.RepoPreset {
	if presets == nil {
		return nil
	}
	out := slices.Clone(presets)
	for i := range out {
		out[i].Repos = slices.Clone(out[i].Repos)
	}
	return out
}

func CloneModeVisibility(modes config.ModeVisibility) config.ModeVisibility {
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
	if modes.Actions != nil {
		v := *modes.Actions
		out.Actions = &v
	}
	if modes.Pulls != nil {
		v := *modes.Pulls
		out.Pulls = &v
	}
	if modes.Issues != nil {
		v := *modes.Issues
		out.Issues = &v
	}
	if modes.Workspaces != nil {
		v := *modes.Workspaces
		out.Workspaces = &v
	}
	return out
}

func CloneConfigAgents(agents []config.Agent) []config.Agent {
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
