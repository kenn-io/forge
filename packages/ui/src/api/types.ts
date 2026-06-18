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
export type RateLimitsResponse = components["schemas"]["RateLimitsResponse"];
export type ActivityItem = components["schemas"]["ActivityItemResponse"];
export type ActivityResponse = components["schemas"]["ActivityResponse"];
export type CommentAutocompleteResponse = components["schemas"]["CommentAutocompleteResponse"];
export type CommentAutocompleteReference = components["schemas"]["CommentAutocompleteReference"];
export type ActivityParams = NonNullable<operations["list-activity"]["parameters"]["query"]>;
export type PullsParams = operations["list-pulls"]["parameters"]["query"];
export type IssuesParams = operations["list-issues"]["parameters"]["query"];
export type MergeParams = components["schemas"]["MergePRInputBody"];

export type WorktreeLink = components["schemas"]["WorktreeLinkResponse"];
export type LaunchTarget = components["schemas"]["LaunchTarget"];
export type RuntimeSession = components["schemas"]["SessionInfo"];
export type WorkspaceRuntime = components["schemas"]["WorkspaceRuntimeResponse"];

export type Label = components["schemas"]["Label"];
export type IssueLabel = Label;
export type RepoLabelsResponse = components["schemas"]["RepoLabelsResponse"];
export type ItemLabelsResponse = components["schemas"]["ItemLabelsResponse"];

export type KanbanStatus = PullRequest["KanbanStatus"];
export type MergeRequestState = PullRequest["State"];

export interface CICheck {
  name: string;
  status: string;
  conclusion: string;
  url: string;
  app: string;
  required?: boolean;
  duration_seconds?: number;
}

export type ActivitySettings = components["schemas"]["Activity"];
export type TerminalSettings = components["schemas"]["Terminal"];
export type TerminalRenderer = TerminalSettings["renderer"];
export type ModeVisibility = components["schemas"]["ModeVisibility"];

export const DEFAULT_TERMINAL_SETTINGS: TerminalSettings = {
  font_family: "",
  font_size: 14,
  scrollback: 1000,
  line_height: 1,
  letter_spacing: 0,
  cursor_blink: true,
  font_ligatures: false,
  renderer: "xterm",
  hide_tmux_status: false,
};

export const DEFAULT_MODE_VISIBILITY: ModeVisibility = {
  activity: true,
  repos: true,
  kata: false,
  docs: false,
  messages: false,
  pulls: true,
  issues: true,
  board: true,
  reviews: true,
  workspaces: true,
};

export type AgentSettings = components["schemas"]["Agent"];
export type ConfigRepo = components["schemas"]["ConfiguredRepoStatus"];
export type Settings = components["schemas"]["SettingsResponse"];
export type FleetSettings = components["schemas"]["FleetSettingsResponse"];
export type FleetSettingsUpdate = components["schemas"]["UpdateFleetSettingsInputBody"];
export type FleetPeer = components["schemas"]["FleetPeer"];
export type FleetSSHPeer = components["schemas"]["FleetSSHPeer"];

export interface DiffResult {
  stale: boolean;
  whitespace_only_count: number;
  files: DiffFile[];
}

export interface FilesResult {
  stale: boolean;
  whitespace_only_count?: number;
  files: DiffFile[];
}

export type FilePreview = components["schemas"]["FilePreviewResponse"];
export type DiffFileSide = "old" | "new";

export interface DiffFile {
  path: string;
  old_path: string;
  status: "added" | "modified" | "deleted" | "renamed" | "copied";
  is_binary: boolean;
  is_generated?: boolean;
  is_whitespace_only: boolean;
  additions: number;
  deletions: number;
  patch: string;
  hunks: DiffHunk[];
}

export interface DiffHunk {
  old_start: number;
  old_count: number;
  new_start: number;
  new_count: number;
  section?: string;
  lines: DiffLine[];
}

export interface DiffLine {
  type: "context" | "add" | "delete";
  content: string;
  old_num?: number;
  new_num?: number;
  no_newline?: boolean;
}

export interface CommitInfo {
  sha: string;
  message: string;
  author_name: string;
  authored_at: string;
}

export interface WorkspaceHost {
  key: string;
  label: string;
  connectionState: "connected" | "connecting" | "disconnected" | "error";
  transport?: "ssh" | "local";
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

export interface WorkspaceData {
  hosts: WorkspaceHost[];
  selectedWorktreeKey: string | null;
  selectedHostKey: string | null;
}

export interface WorkspaceDetailContext {
  worktree: WorkspaceWorktree | null;
  project: WorkspaceProject | null;
  host: WorkspaceHost | null;
}
