package fleet

// SchemaVersion is the fleet snapshot contract version (raw + enriched).
// v2 covers the breaking session backend->runtimeKind rename plus the
// expanded session/host/worktree fields; the hub rejects peers whose
// schemaVersion differs (see internal/server fan-out).
const SchemaVersion = 2

// ---- raw layer (scoped keys, no UUIDs; hub<->peer wire shape) ----

type RawHost struct {
	Hostname         string            `json:"hostname"`
	Platform         string            `json:"platform"` // "linux" | "macos"
	Version          string            `json:"version,omitempty"`
	LastSeenAt       string            `json:"lastSeenAt,omitempty"`
	TmuxLastPolledAt string            `json:"tmuxLastPolledAt,omitempty"`
	TmuxProbeError   string            `json:"tmuxProbeError,omitempty"`
	TmuxMetricsError string            `json:"tmuxMetricsError,omitempty"`
	TmuxSessions     []TmuxSessionInfo `json:"tmuxSessions,omitempty"`
}

type TmuxWindowInfo struct {
	ID       string `json:"id"`
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Activity string `json:"activity,omitempty"`
}

type TmuxSessionInfo struct {
	Name             string           `json:"name"`
	Managed          bool             `json:"managed"`
	WorktreeKey      string           `json:"worktreeKey,omitempty"`
	SessionScopedKey string           `json:"sessionScopedKey,omitempty"`
	Windows          []TmuxWindowInfo `json:"windows"`
	WindowCount      int              `json:"windowCount"`
	CreatedAt        string           `json:"createdAt,omitempty"`
}

type RawSnapshot struct {
	SchemaVersion         int             `json:"schemaVersion"`
	Generation            uint64          `json:"generation"`
	Host                  RawHost         `json:"host"`
	PlatformAuthenticated *bool           `json:"platformAuthenticated,omitempty"`
	Capabilities          *Capabilities   `json:"capabilities,omitempty"`
	Projects              []RawProject    `json:"projects,omitempty"`
	Worktrees             []RawWorktree   `json:"worktrees,omitempty"`
	Sessions              []RawSession    `json:"sessions,omitempty"`
	RemoteHosts           []RawRemoteHost `json:"remoteHosts,omitempty"`
}

type RawProject struct {
	HostKey   string `json:"hostKey,omitempty"`
	ScopedKey string `json:"scopedKey"`
	// RegistryID is the producer's local registry id for this project (empty
	// for a synthesized project, which has no registry row). A client mutates
	// the project by this id rather than by scoped key.
	RegistryID    string `json:"registryId,omitempty"`
	Name          string `json:"name"`
	RootPath      string `json:"rootPath"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	// Platform is the provider kind ("github", "gitlab", "forgejo",
	// "gitea") of the project's platform identity. Together with
	// PlatformHost and PlatformRepo it lets a client build provider-aware
	// routes and distinguish same-host identities across providers.
	Platform       string `json:"platform,omitempty"`
	PlatformRepo   string `json:"platformRepo,omitempty"`
	PlatformHost   string `json:"platformHost,omitempty"`
	IsStale        bool   `json:"isStale,omitempty"`
	RepositoryKind string `json:"repositoryKind,omitempty"`
	BackendReady   *bool  `json:"backendReady,omitempty"`
	// IsSynthesized marks a project with no registered local checkout —
	// synthesized only to anchor an orphan workspace's worktree. Such a
	// project has no rootPath or repositoryKind; consumers must treat it as
	// read-only (no worktree creation) rather than a registered project.
	IsSynthesized bool `json:"isSynthesized,omitempty"`
}

type RawWorktree struct {
	HostKey    string `json:"hostKey,omitempty"`
	ScopedKey  string `json:"scopedKey"`
	ProjectKey string `json:"projectKey"`
	// RegistryID is the producer's local registry id for this worktree (empty
	// for a synthesized primary root worktree or a workspace-only overlay,
	// neither of which has a registry row). A client mutates the worktree by
	// this id rather than by scoped key.
	RegistryID     string  `json:"registryId,omitempty"`
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	Branch         string  `json:"branch,omitempty"`
	IsPrimary      bool    `json:"isPrimary,omitempty"`
	IsStale        bool    `json:"isStale,omitempty"`
	DiffAdded      *int    `json:"diffAdded,omitempty"`
	DiffRemoved    *int    `json:"diffRemoved,omitempty"`
	SyncAhead      *int    `json:"syncAhead,omitempty"`
	SyncBehind     *int    `json:"syncBehind,omitempty"`
	LinkedPRNumber *int    `json:"linkedPRNumber,omitempty"`
	PRState        *string `json:"prState,omitempty"`
	PRTitle        *string `json:"prTitle,omitempty"`
	ChecksStatus   *string `json:"checksStatus,omitempty"`

	IsHidden           bool          `json:"isHidden,omitempty"`
	PRURL              *string       `json:"prURL,omitempty"`
	PRUpdatedAt        *string       `json:"prUpdatedAt,omitempty"`
	ChecksDetail       []CheckDetail `json:"checksDetail,omitempty"`
	LastPolledAt       *string       `json:"lastPolledAt,omitempty"`
	SessionBackend     string        `json:"sessionBackend,omitempty"`
	LinkedIssueNumbers []int         `json:"linkedIssueNumbers,omitempty"`
}

// Session backend vocabulary for RawWorktree.SessionBackend and
// WorktreeSummary.SessionBackend. These are generic terminal-backend
// descriptors: a local PTY-owner-managed terminal, a local tmux session, or a
// tmux session reached on a remote host over SSH. The producer emits the local
// variants from a worktree's own backend; the remote variant is stamped when a
// worktree is surfaced from a remote peer.
const (
	SessionBackendLocalPTY   = "localPTY"
	SessionBackendLocalTmux  = "localTmux"
	SessionBackendRemoteTmux = "remoteTmux"
)

type RawSession struct {
	HostKey        string   `json:"hostKey,omitempty"`
	ScopedKey      string   `json:"scopedKey"`
	WorktreeKey    string   `json:"worktreeKey,omitempty"`
	Status         string   `json:"status"`
	RuntimeKind    string   `json:"runtimeKind,omitempty"`
	SessionKind    string   `json:"sessionKind,omitempty"`
	Role           string   `json:"role,omitempty"`
	Label          string   `json:"label,omitempty"`
	ExecutableName string   `json:"executableName,omitempty"`
	AgentKind      string   `json:"agentKind,omitempty"`
	CPUPercent     *float64 `json:"cpuPercent,omitempty"`
	ResidentMB     *int     `json:"residentMB,omitempty"`
	ProcessCount   *int     `json:"processCount,omitempty"`
	LastOutputAt   *string  `json:"lastOutputAt,omitempty"`
	LastActiveAt   *string  `json:"lastActiveAt,omitempty"`
}

type RawRemoteHost struct {
	HostKey               string            `json:"hostKey"`
	Name                  string            `json:"name"`
	BaseURL               string            `json:"baseURL,omitempty"`
	Platform              string            `json:"platform,omitempty"`
	Reachable             bool              `json:"reachable"`
	PlatformAuthenticated *bool             `json:"platformAuthenticated,omitempty"`
	Generation            uint64            `json:"generation,omitempty"`
	Version               string            `json:"version,omitempty"`
	LastSeenAt            string            `json:"lastSeenAt,omitempty"`
	TmuxLastPolledAt      string            `json:"tmuxLastPolledAt,omitempty"`
	TmuxProbeError        string            `json:"tmuxProbeError,omitempty"`
	TmuxMetricsError      string            `json:"tmuxMetricsError,omitempty"`
	Error                 *string           `json:"error,omitempty"`
	Capabilities          *Capabilities     `json:"capabilities,omitempty"`
	PreferredTransport    string            `json:"preferredTransport,omitempty"`
	SSHDestination        *string           `json:"sshDestination,omitempty"`
	TmuxSessions          []TmuxSessionInfo `json:"tmuxSessions,omitempty"`
}

// ---- capabilities + diagnostics ----

type CommandCapabilities struct {
	WorktreeCreate   bool `json:"worktreeCreate"`
	WorktreeImportPR bool `json:"worktreeImportPullRequest"`
	WorktreeDelete   bool `json:"worktreeDelete"`
	SessionEnsure    bool `json:"sessionEnsure"`
	SessionKill      bool `json:"sessionKill"`
	RepositoryClone  bool `json:"repositoryClone"`
	ProjectAdd       bool `json:"projectAdd"`
	ProjectRemove    bool `json:"projectRemove"`
}

type DependencyCapabilities struct {
	Git  bool `json:"git"`
	Gh   bool `json:"gh"`
	Tmux bool `json:"tmux"`
}

type FeatureCapabilities struct {
	ResourceMetrics bool   `json:"resourceMetrics"`
	SetupHook       bool   `json:"setupHook"`
	TeardownHook    bool   `json:"teardownHook"`
	MoshAttach      bool   `json:"moshAttach"`
	TmuxVersion     string `json:"tmuxVersion,omitempty"`
}

// Capabilities groups command, dependency, and feature availability for a host.
type Capabilities struct {
	Commands     CommandCapabilities    `json:"commands"`
	Dependencies DependencyCapabilities `json:"dependencies"`
	Features     FeatureCapabilities    `json:"features"`
}

type HostDiagnostic struct {
	Code               string   `json:"code"`
	Severity           string   `json:"severity"`
	Summary            string   `json:"summary"`
	RecoverySuggestion string   `json:"recoverySuggestion"`
	BlocksOperations   []string `json:"blocksOperations"`
}

type HostOperationAvailability struct {
	Available         bool    `json:"available"`
	UnavailableReason *string `json:"unavailableReason,omitempty"`
}

// ---- enriched layer (UUIDs, client-ready) ----

// Snapshot is the enriched client-ready snapshot envelope.
// Populated by the snapshot builder.
type Snapshot struct {
	SchemaVersion         int               `json:"schemaVersion"`
	Generation            uint64            `json:"generation"`
	PlatformAuthenticated *bool             `json:"platformAuthenticated,omitempty"`
	ActivePlatformHost    *string           `json:"activePlatformHost,omitempty"`
	Hosts                 []HostSummary     `json:"hosts"`
	Projects              []ProjectSummary  `json:"projects"`
	Worktrees             []WorktreeSummary `json:"worktrees"`
	Sessions              []SessionSummary  `json:"sessions"`
	ProjectMap            map[string]string `json:"projectMap,omitempty"`
}

type HostSummary struct {
	ID                    string                               `json:"id"`
	ConfigKey             string                               `json:"configKey"`
	Name                  string                               `json:"name"`
	Kind                  string                               `json:"kind"`
	Platform              string                               `json:"platform"`
	SSHDestination        *string                              `json:"sshDestination,omitempty"`
	PreferredTransport    string                               `json:"preferredTransport"`
	Reachable             bool                                 `json:"reachable"`
	LastSeenAt            *string                              `json:"lastSeenAt,omitempty"`
	Hostname              *string                              `json:"hostname,omitempty"`
	Version               *string                              `json:"version,omitempty"`
	TmuxLastPolledAt      *string                              `json:"tmuxLastPolledAt,omitempty"`
	TmuxProbeError        string                               `json:"tmuxProbeError,omitempty"`
	TmuxMetricsError      string                               `json:"tmuxMetricsError,omitempty"`
	Capabilities          *Capabilities                        `json:"capabilities,omitempty"`
	Diagnostics           []HostDiagnostic                     `json:"diagnostics"`
	OperationAvailability map[string]HostOperationAvailability `json:"operationAvailability"`
	Error                 *string                              `json:"error,omitempty"`
	ConnectionState       *string                              `json:"connectionState,omitempty"`
	TmuxSessions          []TmuxSessionInfo                    `json:"tmuxSessions"`
}

type ProjectSummary struct {
	ID        string `json:"id"`
	HostID    string `json:"hostID"`
	ScopedKey string `json:"scopedKey"`
	// RegistryID is the producer's local registry id (see RawProject.RegistryID):
	// the id a client mutates the project by. Empty for a synthesized project.
	RegistryID     string `json:"registryID,omitempty"`
	Name           string `json:"name"`
	RootPath       string `json:"rootPath"`
	RepositoryKind string `json:"repositoryKind"`
	DefaultBranch  string `json:"defaultBranch"`
	// Platform is the provider kind of the project's platform identity
	// (see RawProject.Platform).
	Platform         string  `json:"platform,omitempty"`
	PlatformURL      *string `json:"platformURL,omitempty"`
	PlatformCoverage *string `json:"platformCoverage,omitempty"`
	IsStale          bool    `json:"isStale,omitempty"`
	// IsSynthesized marks a project with no registered local checkout (see
	// RawProject.IsSynthesized): rootPath/repositoryKind are empty and the
	// consumer must treat it as read-only.
	IsSynthesized bool `json:"isSynthesized,omitempty"`
}

type WorktreeSummary struct {
	ID        string `json:"id"`
	HostID    string `json:"hostID"`
	ProjectID string `json:"projectID"`
	ScopedKey string `json:"scopedKey"`
	// RegistryID is the producer's local registry id (see RawWorktree.RegistryID):
	// the id a client mutates the worktree by. Empty for a synthesized primary
	// root worktree or a workspace-only overlay.
	RegistryID         string        `json:"registryID,omitempty"`
	Name               string        `json:"name"`
	Path               string        `json:"path"`
	Branch             string        `json:"branch"`
	IsPrimary          bool          `json:"isPrimary,omitempty"`
	IsHidden           bool          `json:"isHidden,omitempty"`
	IsStale            bool          `json:"isStale,omitempty"`
	DiffAdded          *int          `json:"diffAdded,omitempty"`
	DiffRemoved        *int          `json:"diffRemoved,omitempty"`
	SyncAhead          *int          `json:"syncAhead,omitempty"`
	SyncBehind         *int          `json:"syncBehind,omitempty"`
	LinkedPRNumber     *int          `json:"linkedPRNumber,omitempty"`
	PRState            *string       `json:"prState,omitempty"`
	PRURL              *string       `json:"prURL,omitempty"`
	PRTitle            *string       `json:"prTitle,omitempty"`
	PRUpdatedAt        *string       `json:"prUpdatedAt,omitempty"`
	ChecksStatus       *string       `json:"checksStatus,omitempty"`
	ChecksDetail       []CheckDetail `json:"checksDetail,omitempty"`
	LastPolledAt       *string       `json:"lastPolledAt,omitempty"`
	SessionBackend     string        `json:"sessionBackend"`
	LinkedIssueNumbers []int         `json:"linkedIssueNumbers"`
}

type CheckDetail struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	URL        string `json:"url,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

type SessionSummary struct {
	ID             string   `json:"id"`
	HostID         string   `json:"hostID"`
	WorktreeID     *string  `json:"worktreeID,omitempty"`
	ScopedKey      string   `json:"scopedKey"`
	RuntimeKind    string   `json:"runtimeKind"`
	Status         string   `json:"status"`
	SessionKind    string   `json:"sessionKind,omitempty"`
	Role           string   `json:"role,omitempty"`
	ExecutableName string   `json:"executableName,omitempty"`
	AgentKind      string   `json:"agentKind,omitempty"`
	CPUPercent     *float64 `json:"cpuPercent,omitempty"`
	ResidentMB     *int     `json:"residentMB,omitempty"`
	ProcessCount   *int     `json:"processCount,omitempty"`
	LastOutputAt   *string  `json:"lastOutputAt,omitempty"`
	LastActiveAt   *string  `json:"lastActiveAt,omitempty"`
}
