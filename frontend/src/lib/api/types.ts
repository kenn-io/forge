import type { components, operations } from "./generated/schema.js";

export type Repo = components["schemas"]["RepoResponse"];
export type RepoSummary = components["schemas"]["RepoSummaryResponse"];
export type RepoSummaryAuthor = components["schemas"]["RepoSummaryAuthorResponse"];
export type RepoSummaryIssue = components["schemas"]["RepoSummaryIssueResponse"];
export type RepoSummaryCommitPointResponse = components["schemas"]["RepoSummaryCommitPointResponse"];
export type RepoSummaryReleaseResponse = components["schemas"]["RepoSummaryReleaseResponse"];
export type PullRequest = components["schemas"]["MergeRequestResponse"];
export type ProviderCapabilities = components["schemas"]["ProviderCapabilitiesResponse"];
export type OperationAvailability = components["schemas"]["OperationAvailability"];
export type RepoOperations = components["schemas"]["RepoOperations"];
export type Issue = components["schemas"]["IssueResponse"];
export type IssueEvent = components["schemas"]["IssueEvent"];
export type IssueDetail = components["schemas"]["IssueDetailResponse"];
export type PREvent = components["schemas"]["MergeRequestEventResponse"];
export type PullDetail = components["schemas"]["MergeRequestDetailResponse"];
export type SyncStatus = components["schemas"]["SyncStatus"];
export type RateLimitHostStatus = components["schemas"]["RateLimitHostStatus"];
export type RateLimitResourceStatus = components["schemas"]["RateLimitResourceStatus"];
export type LocalSyncCeilingStatus = components["schemas"]["LocalSyncCeilingStatus"];
export type RateLimitsResponse = components["schemas"]["RateLimitsResponse"];
export type ActivityItem = components["schemas"]["ActivityItemResponse"];
export type ActivityResponse = components["schemas"]["ActivityResponse"];
export type ActivitySubject = components["schemas"]["ActivitySubjectResponse"];
export type WorkspaceActivitySubject = components["schemas"]["WorkspaceActivitySubjectResponse"];
export type ActivityAuthorsResponse = components["schemas"]["ActivityAuthorsResponse"];
export type NotificationsResponse = components["schemas"]["NotificationsResponse"];
export type NotificationBulkResponse = components["schemas"]["NotificationBulkResponse"];
export type CommentAutocompleteResponse = components["schemas"]["CommentAutocompleteResponse"];
export type CommentAutocompleteReference = components["schemas"]["CommentAutocompleteReference"];
export type RepoBrowserBlob = components["schemas"]["RepoBrowserBlob"];
export type RepoBrowserCommit = components["schemas"]["RepoBrowserCommit"];
export type RepoBrowserRef = components["schemas"]["RepoBrowserRef"];
export type RepoBrowserRefsResponse = components["schemas"]["RepoBrowserRefsResponse"];
export type RepoBrowserTreeEntry = components["schemas"]["RepoBrowserTreeEntry"];
export type ActivityParams = NonNullable<operations["list-activity"]["parameters"]["query"]>;
export type ActivityAuthorsParams = NonNullable<operations["list-activity-authors"]["parameters"]["query"]>;
export type PullsParams = operations["list-pulls"]["parameters"]["query"];
export type IssuesParams = operations["list-issues"]["parameters"]["query"];
export type ApprovePRInputBody = components["schemas"]["ApprovePRInputBody"];
export type RequestChangesPRInputBody = components["schemas"]["RequestChangesPRInputBody"];
export type MergeParams = components["schemas"]["MergePRInputBody"];
export type EditPRContentInputBody = components["schemas"]["EditPRContentInputBody"];
export type StarredRequest = components["schemas"]["StarredRequest"];
export type UnsetStarredParams = operations["unset-starred"]["parameters"]["query"];
export type GithubStateInputBody = components["schemas"]["GithubStateInputBody"];

export type LaunchTarget = components["schemas"]["LaunchTarget"];
export type RuntimeSession = components["schemas"]["SessionInfo"];
export type WorkspaceRuntime = components["schemas"]["WorkspaceRuntimeResponse"];

export type Label = components["schemas"]["Label"];
export type RepoLabelsResponse = components["schemas"]["RepoLabelsResponse"];
export type ItemLabelsResponse = components["schemas"]["ItemLabelsResponse"];

export type KanbanStatus = PullRequest["KanbanStatus"];

export type CICheckWire = components["schemas"]["CICheck"];
export type CICheck = CICheckWire & { readonly required?: boolean };

export type ActivitySettings = components["schemas"]["Activity"];
export type IssueSettings = components["schemas"]["Issues"];
export type PullRequestSettings = components["schemas"]["PullRequests"];
export type DetailSettings = components["schemas"]["Detail"];
export type TerminalSettings = components["schemas"]["Terminal"];
export type ModeVisibility = components["schemas"]["ModeVisibility"];

export const DEFAULT_TERMINAL_SETTINGS: TerminalSettings = {
  font_family: "",
  font_size: 12,
  scrollback: 1000,
  line_height: 1,
  letter_spacing: 0,
  cursor_blink: true,
  font_ligatures: false,
  hide_tmux_status: false,
  graphics: true,
  tmux_mouse: true,
  retained_sessions: 10,
};

export const DEFAULT_MODE_VISIBILITY: ModeVisibility = {
  activity: true,
  repos: true,
  docs: false,
  actions: false,
  pulls: true,
  issues: true,
  reviews: true,
  workspaces: true,
};

export const DEFAULT_PULL_REQUEST_SETTINGS: PullRequestSettings = {
  allow_mid_stack_merges: false,
  prefer_github_native_stacks: false,
};

export const DEFAULT_DETAIL_SETTINGS: DetailSettings = {
  initial_timeline_entry_limit: 50,
  collapse_single_line_breaks: false,
  render_commit_messages_as_markdown: false,
};

export type AgentSettings = components["schemas"]["Agent"];
export type ConfigRepo = components["schemas"]["ConfiguredRepoStatus"];
export type RepoPreset = components["schemas"]["RepoPreset"];
export type KataProjectRepoMapping = components["schemas"]["KataProjectRepoMapping"];
export type WorkspaceKataMetadata = components["schemas"]["WorkspaceKataMetadata"];
type SettingsResponse = components["schemas"]["SettingsResponse"];
export type Settings = Omit<SettingsResponse, "notifications"> & {
  notifications?: SettingsResponse["notifications"];
};
export type FleetSettings = components["schemas"]["FleetSettingsResponse"];
export type FleetSettingsUpdate = components["schemas"]["UpdateFleetSettingsInputBody"];
export type MCPSettings = components["schemas"]["McpSettingsResponse"];
export type MCPSettingsUpdate = components["schemas"]["McpSettingsUpdate"];

export type FilePreview = components["schemas"]["FilePreviewResponse"];
export type DiffResponseWire = components["schemas"]["DiffResponse"];
export type FilesResponseWire = components["schemas"]["FilesResponse"];
export type DiffFileWire = components["schemas"]["DiffFile"];
export type DiffHunkWire = components["schemas"]["Hunk"];
export type DiffLineWire = components["schemas"]["Line"];

export type DiffLine = Omit<DiffLineWire, "type"> & {
  type: "context" | "add" | "delete";
};

export type DiffHunk = Omit<DiffHunkWire, "lines"> & {
  lines: DiffLine[];
};

export type DiffFile = Omit<DiffFileWire, "status" | "hunks"> & {
  status: "added" | "modified" | "deleted" | "renamed" | "copied";
  hunks: DiffHunk[];
};

export type DiffResult = Omit<DiffResponseWire, "files"> & {
  files: DiffFile[];
};

export type FilesResult = Omit<FilesResponseWire, "files"> & {
  files: DiffFile[];
};

export interface CommitInfo {
  sha: string;
  message: string;
  author_name: string;
  authored_at: string;
  // True when the commit is reachable from the workspace branch's upstream
  // tracking ref; false means it is local-only. Absent when push status is
  // unknown, such as pull request commits.
  pushed?: boolean;
}

export interface WorkspaceHost {
  key: string;
  label: string;
  connectionState: "connected" | "connecting" | "disconnected" | "error";
  transport?: "http" | "local";
  platform?: string;
  projects: WorkspaceProject[];
  sessions: WorkspaceSession[];
  resources: WorkspaceResources | null;
}

export interface WorkspaceProject {
  key: string;
  name: string;
  kind: "repository" | "scratch";
  repoKind: string;
  defaultBranch: string;
  platformRepo: string | null;
  platformURL?: string;
  worktrees: WorkspaceWorktree[];
}

export interface WorkspaceWorktree {
  key: string;
  name: string;
  branch: string;
  isPrimary: boolean;
  isHidden: boolean;
  isStale: boolean;
  sessionBackend: string | null;
  linkedPR: WorkspaceLinkedPR | null;
  activity: WorkspaceActivity;
  diff: WorkspaceDiff | null;
}

export interface WorkspaceLinkedPR {
  number: number;
  title: string;
  state: "open" | "closed" | "merged";
  checksStatus: string | null;
  updatedAt: string | null;
}

export interface WorkspaceActivity {
  state: "idle" | "active" | "running" | "needsAttention";
  lastOutputAt: string | null;
}

export interface WorkspaceDiff {
  added: number;
  removed: number;
}

export interface WorkspaceSession {
  key: string;
  name: string;
  worktreeKey: string | null;
  isHidden: boolean;
}

export interface WorkspaceResources {
  cpuPercent: number;
  residentMB: number;
}
