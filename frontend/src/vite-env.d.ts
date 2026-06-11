/// <reference types="vite/client" />

declare module "*.svelte?retry" {
  const component: any;
  export default component;
}

declare module "*.svelte?retry2" {
  const component: any;
  export default component;
}

declare module "@xterm/addon-ligatures/lib/addon-ligatures.mjs" {
  export { LigaturesAddon } from "@xterm/addon-ligatures";
}

interface MiddlemanConfig {
  theme?: {
    mode?: "light" | "dark" | "system";
    colors?: Partial<{
      bgPrimary: string;
      bgSurface: string;
      bgSurfaceHover: string;
      bgInset: string;
      borderDefault: string;
      borderMuted: string;
      textPrimary: string;
      textSecondary: string;
      textMuted: string;
      accentBlue: string;
      accentAmber: string;
      accentPurple: string;
      accentGreen: string;
      accentRed: string;
      accentTeal: string;
      overlayBg: string;
      shadowSm: string;
      shadowMd: string;
      shadowLg: string;
      kanbanNew: string;
      kanbanReviewing: string;
      kanbanWaiting: string;
      kanbanAwaitingMerge: string;
    }>;
    fonts?: Partial<{
      sans: string;
      mono: string;
    }>;
    radii?: Partial<{
      sm: string;
      md: string;
      lg: string;
    }>;
  };
  ui?: {
    hideSync?: boolean;
    hideRepoSelector?: boolean;
    hideStar?: boolean;
    sidebarCollapsed?: boolean;
    repo?: { owner: string; name: string };
    host?: string;
    activeWorktreeKey?: string;
  };
  actions?: {
    pullRequest?: ActionHookDef[];
    issue?: ActionHookDef[];
    project?: ProjectActionDef[];
  };
  workspace?: WorkspaceData;
  onWorkspaceCommand?: WorkspaceCommandHandler;
  embed?: {
    hideHeader?: boolean;
    hideStatusBar?: boolean;
    initialRoute?: string;
    sidebarWidth?: number;
    activePlatformHost?: string | null;
    panelMode?: boolean;
    hoverCardsEnabled?: boolean;
    tooling?: ToolingStatus;
  };
  onLayoutChanged?: (layout: { sidebar: { width: number }; pinnedPanel: { width: number; visible: boolean } }) => void;
  onNavigate?: (event: MiddlemanNavigateEvent) => void;
  onRouteChange?: (event: MiddlemanNavigateEvent) => void;
}

interface ActionHookDef {
  id: string;
  label: string;
  handler: (context: {
    surface: string;
    owner: string;
    name: string;
    number: number;
    meta?: Record<string, unknown>;
  }) => void | Promise<void>;
}

// ProjectActionDef is the registry shape for project-scoped actions
// (add-existing, clone, connect-github, new-worktree). The handler MUST
// return a CommandResult so the firing surface can render success/failure
// instead of a fire-and-forget click. The action ID is the identifier the
// surface uses to look up the handler.
interface ProjectActionDef {
  id: string;
  label: string;
  handler: (context: {
    surface: string;
    projectId?: string;
    meta?: Record<string, unknown>;
  }) => CommandResult | Promise<CommandResult>;
}

// ToolingStatus reports the embedding host's view of git/gh availability.
// The First Run Panel and the New Worktree sheet read this to gate the
// GitHub-dependent surfaces and surface specific recovery copy when a
// tool is missing.
interface ToolingStatus {
  git?: {
    available: boolean;
    version?: string;
  };
  gh?: {
    available: boolean;
    authenticated: boolean;
    user?: string;
    host?: string;
  };
  glab?: {
    available: boolean;
    authenticated: boolean;
    user?: string;
    host?: string;
  };
}

interface WorkspaceHost {
  key: string;
  label: string;
  connectionState: "connected" | "connecting" | "disconnected" | "error";
  transport?: "ssh" | "local";
  platform?: string;
  projects: WorkspaceProject[];
  sessions: WorkspaceSession[];
  resources: WorkspaceResources | null;
}

interface WorkspaceProject {
  key: string;
  name: string;
  kind: "repository" | "scratch";
  repoKind: string;
  defaultBranch: string;
  platformRepo: string | null;
  platformURL?: string;
  worktrees: WorkspaceWorktree[];
}

interface WorkspaceWorktree {
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

interface WorkspaceLinkedPR {
  number: number;
  title: string;
  state: "open" | "closed" | "merged";
  checksStatus: string | null;
  updatedAt: string | null;
}

interface WorkspaceActivity {
  state: "idle" | "active" | "running" | "needsAttention";
  lastOutputAt: string | null;
}

interface WorkspaceDiff {
  added: number;
  removed: number;
}

interface WorkspaceSession {
  key: string;
  name: string;
  worktreeKey: string | null;
  isHidden: boolean;
}

interface WorkspaceResources {
  cpuPercent: number;
  residentMB: number;
}

interface WorkspaceData {
  hosts: WorkspaceHost[];
  selectedWorktreeKey: string | null;
  selectedHostKey: string | null;
}

interface CommandResult {
  ok: boolean;
  message?: string;
}

interface WorkspaceCommandHandler {
  (command: string, payload: Record<string, unknown>): CommandResult | Promise<CommandResult>;
}

interface WorkspaceDetailContext {
  worktree: WorkspaceWorktree | null;
  project: WorkspaceProject | null;
  host: WorkspaceHost | null;
}

interface MiddlemanNavigateEvent {
  type: "pull" | "issue" | "activity" | "repos" | "kata" | "docs" | "messages" | "board" | "reviews" | "workspaces";
  owner?: string;
  name?: string;
  number?: number;
  focus: boolean;
  view: string;
  repo?: string;
  host?: string;
}

interface Window {
  __BASE_PATH__?: string;
  __MIDDLEMAN_DEV_API_URL__?: string;
  __MIDDLEMAN_FORCE_MOBILE_ROUTES__?: boolean;
  __middleman_config?: MiddlemanConfig;
  __middleman_event_source_counts?: () => {
    created: number;
    closed: number;
  };
  __middleman_notify_config_changed?: () => void;
  __middleman_update_workspace?: (data: WorkspaceData) => void;
  __middleman_navigate_to_route?: (route: string) => void;
  __middleman_set_repo_filter?: (repo: { owner: string; name: string } | null) => void;
  __middleman_update_selection?: (selection: { hostKey?: string | null; worktreeKey?: string | null }) => void;
  __middleman_update_host_state?: (
    hostKey: string,
    patch: {
      connectionState?: WorkspaceHost["connectionState"];
      resources?: WorkspaceResources | null;
    },
  ) => void;
  __middleman_update_tooling?: (tooling: ToolingStatus) => void;
}
