import { setGlobalRepo } from "../stores/filter.svelte.js";

// Bridge: repo filter (module-scope, not workspace-specific)
window.__middleman_set_repo_filter = (repo: { owner: string; name: string } | null) => {
  setGlobalRepo(repo ? `${repo.owner}/${repo.name}` : undefined);
};

export interface ActionHook {
  id: string;
  label: string;
  handler: (context: ActionContext) => void | Promise<void>;
}

export interface ActionContext {
  surface: string;
  owner: string;
  name: string;
  number: number;
  meta?: Record<string, unknown>;
}

export interface ProjectActionContext {
  surface: string;
  projectId?: string;
  meta?: Record<string, unknown>;
}

export interface ProjectActionHook {
  id: string;
  label: string;
  handler: (context: ProjectActionContext) => CommandResult | Promise<CommandResult>;
}

// Re-export ToolingStatus from the global ambient module so .svelte
// files can import it explicitly. Lint in .svelte files does not pick
// up ambient globals declared in vite-env.d.ts.
export type ToolingStatusValue = ToolingStatus;

interface UIDefaults {
  hideSync: boolean;
  hideRepoSelector: boolean;
  hideStar: boolean;
  sidebarCollapsed: boolean | undefined;
  repo: { owner: string; name: string } | undefined;
}

const UI_DEFAULTS: UIDefaults = {
  hideSync: false,
  hideRepoSelector: false,
  hideStar: false,
  sidebarCollapsed: undefined,
  repo: undefined,
};

let _generation = $state(0);

function readConfig(): MiddlemanConfig | undefined {
  void _generation; // reactive dependency
  return window.__middleman_config;
}

// Install the notify function on window. The embedder calls this
// after mutating window.__middleman_config.
window.__middleman_notify_config_changed = () => {
  _generation++;
};

export function isEmbedded(): boolean {
  // Embed mode is signaled by the embed block specifically: the
  // daemon also serves a config carrying only daemon-side UI state
  // (ui.activeWorktreeKey), which must not flip the standalone SPA
  // into embedded behavior.
  return readConfig()?.embed !== undefined;
}

export function isHeaderHidden(): boolean {
  return readConfig()?.embed?.hideHeader === true;
}

export function isStatusBarHidden(): boolean {
  return readConfig()?.embed?.hideStatusBar === true;
}

export function getThemeMode(): "light" | "dark" | "system" | undefined {
  return readConfig()?.theme?.mode;
}

export function getThemeColors(): NonNullable<NonNullable<MiddlemanConfig["theme"]>["colors"]> | undefined {
  return readConfig()?.theme?.colors;
}

export function getThemeFonts(): NonNullable<NonNullable<MiddlemanConfig["theme"]>["fonts"]> | undefined {
  return readConfig()?.theme?.fonts;
}

export function getThemeRadii(): NonNullable<NonNullable<MiddlemanConfig["theme"]>["radii"]> | undefined {
  return readConfig()?.theme?.radii;
}

export function getUIConfig(): UIDefaults {
  const ui = readConfig()?.ui;
  if (!ui) return UI_DEFAULTS;
  return {
    hideSync: ui.hideSync ?? false,
    hideRepoSelector: ui.hideRepoSelector ?? false,
    hideStar: ui.hideStar ?? false,
    sidebarCollapsed: ui.sidebarCollapsed,
    repo: ui.repo,
  };
}

export function getActiveWorktreeKey(): string | undefined {
  return readConfig()?.ui?.activeWorktreeKey;
}

export function getHost(): string | undefined {
  return readConfig()?.ui?.host;
}

export function getPullRequestActions(): ActionHook[] {
  return readConfig()?.actions?.pullRequest ?? [];
}

export function getIssueActions(): ActionHook[] {
  return readConfig()?.actions?.issue ?? [];
}

export function getProjectActions(): ProjectActionHook[] {
  return readConfig()?.actions?.project ?? [];
}

export function getProjectAction(id: string): ProjectActionHook | undefined {
  return getProjectActions().find((action) => action.id === id);
}

export function getToolingStatus(): ToolingStatus | undefined {
  return readConfig()?.embed?.tooling;
}

export function getOnNavigate(): ((event: MiddlemanNavigateEvent) => void) | undefined {
  return readConfig()?.onNavigate;
}

export function getOnRouteChange(): ((event: MiddlemanNavigateEvent) => void) | undefined {
  return readConfig()?.onRouteChange;
}

export function invokeAction(action: ActionHook, context: ActionContext): void {
  try {
    const result = action.handler(context);
    Promise.resolve(result).catch((err: unknown) => {
      console.error("Embedding action error:", err);
    });
  } catch (err) {
    console.error("Embedding action error:", err);
  }
}

// invokeProjectAction is the ack-aware project-action runner. The firing
// surface awaits the returned CommandResult to render in-flight, success,
// and failure states - this is the contract that fixes "button click does
// nothing" for project actions. Handlers that throw are normalized into
// { ok: false, message } so callers never see an unhandled rejection.
export async function invokeProjectAction(
  action: ProjectActionHook,
  context: ProjectActionContext,
): Promise<CommandResult> {
  try {
    const result = await action.handler(context);
    if (result && typeof result === "object" && "ok" in result && typeof result.ok === "boolean") {
      return result;
    }
    return { ok: true };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    console.error(`Embedding project action "${action.id}" failed:`, err);
    return { ok: false, message };
  }
}

export function getInitialRoute(): string | undefined {
  return readConfig()?.embed?.initialRoute;
}

export function getSidebarWidth(): number | undefined {
  return readConfig()?.embed?.sidebarWidth;
}

export function getEmbedPanelMode(): boolean {
  return readConfig()?.embed?.panelMode === true;
}

export function getEmbedHoverCardsEnabled(): boolean {
  return readConfig()?.embed?.hoverCardsEnabled === true;
}

export function getEmbedActivePlatformHost(): string | null {
  const value = readConfig()?.embed?.activePlatformHost;
  if (value === undefined) return null;
  return value;
}

export function getOnLayoutChanged(): MiddlemanConfig["onLayoutChanged"] | undefined {
  return readConfig()?.onLayoutChanged;
}

let layoutTimer: ReturnType<typeof setTimeout> | undefined;

export function emitLayoutChanged(layout: {
  sidebar: { width: number };
  pinnedPanel: { width: number; visible: boolean };
}): void {
  clearTimeout(layoutTimer);
  layoutTimer = setTimeout(() => {
    const handler = getOnLayoutChanged();
    if (handler) {
      try {
        handler(layout);
      } catch (e) {
        console.error("[middleman] onLayoutChanged error:", e);
      }
    }
  }, 150);
}

export function getWorkspaceData(): WorkspaceData | undefined {
  return readConfig()?.workspace;
}

export function getOnWorkspaceCommand(): WorkspaceCommandHandler | undefined {
  return readConfig()?.onWorkspaceCommand;
}

export async function emitWorkspaceCommand(command: string, payload: Record<string, unknown>): Promise<CommandResult> {
  const handler = getOnWorkspaceCommand();
  if (!handler) {
    return { ok: true };
  }
  try {
    const result = await handler(command, payload);
    if (result && typeof result === "object" && "ok" in result) {
      return result;
    }
    return { ok: true };
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e);
    console.error(`[middleman] workspace command "${command}" failed:`, e);
    return { ok: false, message };
  }
}

export function initWorkspaceBridge(): void {
  window.__middleman_update_workspace = (data: WorkspaceData) => {
    const config = window.__middleman_config;
    if (config) {
      config.workspace = data;
      window.__middleman_notify_config_changed?.();
    }
  };
  window.__middleman_update_selection = (selection: { hostKey?: string | null; worktreeKey?: string | null }) => {
    const config = window.__middleman_config;
    if (!config?.workspace) return;
    const changingHost = "hostKey" in selection && selection.hostKey !== config.workspace.selectedHostKey;
    const updated = { ...config.workspace };
    if ("hostKey" in selection) {
      updated.selectedHostKey = selection.hostKey ?? null;
    }
    if ("worktreeKey" in selection) {
      updated.selectedWorktreeKey = selection.worktreeKey ?? null;
    } else if (changingHost) {
      updated.selectedWorktreeKey = null;
    }
    config.workspace = updated;
    window.__middleman_notify_config_changed?.();
  };
  window.__middleman_update_host_state = (
    hostKey: string,
    patch: {
      connectionState?: WorkspaceHost["connectionState"];
      resources?: WorkspaceResources | null;
    },
  ) => {
    const config = window.__middleman_config;
    if (!config?.workspace) return;
    const hostIdx = config.workspace.hosts.findIndex((h) => h.key === hostKey);
    if (hostIdx < 0) return;
    const host = config.workspace.hosts[hostIdx]!;
    const updated = { ...host };
    if ("connectionState" in patch) {
      updated.connectionState = patch.connectionState!;
    }
    if ("resources" in patch) {
      updated.resources = patch.resources ?? null;
    }
    const hosts = [...config.workspace.hosts];
    hosts[hostIdx] = updated;
    config.workspace = { ...config.workspace, hosts };
    window.__middleman_notify_config_changed?.();
  };
  window.__middleman_update_tooling = (tooling: ToolingStatus) => {
    const config = window.__middleman_config;
    if (!config) return;
    const embed = { ...(config.embed ?? {}), tooling };
    config.embed = embed;
    window.__middleman_notify_config_changed?.();
  };
}
