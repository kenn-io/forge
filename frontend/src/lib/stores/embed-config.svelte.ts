import { Effect, Schema } from "effect";
import type { AppExecution, AppRuntime } from "../app/runtime.js";
import { setGlobalRepo } from "../stores/filter.svelte.js";

class EmbeddingCallbackError extends Schema.TaggedErrorClass<EmbeddingCallbackError>()("EmbeddingCallbackError", {
  operation: Schema.String,
  cause: Schema.Defect(),
}) {}

export class InvalidEmbeddingAcknowledgement extends Schema.TaggedErrorClass<InvalidEmbeddingAcknowledgement>()(
  "InvalidEmbeddingAcknowledgement",
  {
    operation: Schema.String,
    cause: Schema.Defect(),
  },
) {}

const CommandResultSchema = Schema.Struct({
  ok: Schema.Boolean,
  message: Schema.optionalKey(Schema.String),
});

const decodeCommandResult = Effect.fn("Embedding.decodeCommandResult")(function* (operation: string, input: unknown) {
  return yield* Schema.decodeUnknownEffect(CommandResultSchema)(input).pipe(
    Effect.mapError((cause) => InvalidEmbeddingAcknowledgement.make({ operation, cause })),
  );
});

// Bridge: repo filter (module-scope, not workspace-specific)
window.__kenn_forge_set_repo_filter = (repo: { owner: string; name: string } | null) => {
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
  hostKey?: string;
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

type UIRepoConfig = NonNullable<NonNullable<ForgeConfig["ui"]>["repo"]>;

interface UIDefaults {
  hideSync: boolean;
  hideRepoSelector: boolean;
  hideStar: boolean;
  sidebarCollapsed: boolean | undefined;
  repo: UIRepoConfig | undefined;
}

const UI_DEFAULTS: UIDefaults = {
  hideSync: false,
  hideRepoSelector: false,
  hideStar: false,
  sidebarCollapsed: undefined,
  repo: undefined,
};

let _generation = $state(0);

function readConfig(): ForgeConfig | undefined {
  void _generation; // reactive dependency
  return window.__kenn_forge_config;
}

// Install the notify function on window. The embedder calls this
// after mutating window.__kenn_forge_config.
window.__kenn_forge_notify_config_changed = () => {
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

export function getThemeColors(): NonNullable<NonNullable<ForgeConfig["theme"]>["colors"]> | undefined {
  return readConfig()?.theme?.colors;
}

export function getThemeFonts(): NonNullable<NonNullable<ForgeConfig["theme"]>["fonts"]> | undefined {
  return readConfig()?.theme?.fonts;
}

export function getThemeRadii(): NonNullable<NonNullable<ForgeConfig["theme"]>["radii"]> | undefined {
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

export function getOnNavigate(): ((event: ForgeNavigateEvent) => void) | undefined {
  return readConfig()?.onNavigate;
}

export function getOnRouteChange(): ((event: ForgeNavigateEvent) => void) | undefined {
  return readConfig()?.onRouteChange;
}

export function invokeAction(runtime: AppRuntime, action: ActionHook, context: ActionContext): void {
  runtime.runCommand(
    Effect.tryPromise({
      try: () => Promise.resolve(action.handler(context)),
      catch: (cause) => EmbeddingCallbackError.make({ operation: `run embed action ${action.id}`, cause }),
    }).pipe(
      Effect.catchTag("EmbeddingCallbackError", (failure) =>
        Effect.sync(() => console.error("Embedding action error:", failure.cause)),
      ),
    ),
    { operation: "run embedding action", safeContext: { actionId: action.id }, onFailure: () => {} },
  );
}

// Project actions remain an embedding callback boundary. The Effect owns the
// acknowledgement lifetime and turns thrown/rejected handlers into the same
// visible command result as an explicit host rejection.
export const invokeProjectAction = Effect.fn("Embedding.invokeProjectAction")(function* (
  action: ProjectActionHook,
  context: ProjectActionContext,
) {
  const result = yield* Effect.tryPromise({
    try: () => Promise.resolve(action.handler(context)),
    catch: (cause) => EmbeddingCallbackError.make({ operation: `run project action ${action.id}`, cause }),
  }).pipe(
    Effect.catchTag("EmbeddingCallbackError", (failure) =>
      Effect.sync(() => {
        const message = failure.cause instanceof Error ? failure.cause.message : String(failure.cause);
        console.error(`Embedding project action "${action.id}" failed:`, failure.cause);
        return { ok: false, message };
      }),
    ),
  );
  return yield* decodeCommandResult(`project action ${action.id}`, result);
});

export function getInitialRoute(): string | undefined {
  return readConfig()?.embed?.initialRoute;
}

export function getSidebarWidth(): number | undefined {
  return readConfig()?.embed?.sidebarWidth;
}

export function getOnLayoutChanged(): ForgeConfig["onLayoutChanged"] | undefined {
  return readConfig()?.onLayoutChanged;
}

let layoutExecution: AppExecution<void, never> | undefined;

export function emitLayoutChanged(
  runtime: AppRuntime,
  layout: {
    sidebar: { width: number };
    pinnedPanel: { width: number; visible: boolean };
  },
): void {
  layoutExecution?.interrupt();
  layoutExecution = runtime.runCommand(
    Effect.sleep("150 millis").pipe(
      Effect.andThen(
        Effect.try({
          try: () => getOnLayoutChanged()?.(layout),
          catch: (cause) => EmbeddingCallbackError.make({ operation: "publish embed layout", cause }),
        }),
      ),
      Effect.catchTag("EmbeddingCallbackError", (failure) =>
        Effect.sync(() => console.error("[kenn-forge] onLayoutChanged error:", failure.cause)),
      ),
      Effect.asVoid,
    ),
    { operation: "publish embedding layout", safeContext: {}, onFailure: () => {} },
  );
}

export function getWorkspaceData(): WorkspaceData | undefined {
  return readConfig()?.workspace;
}

export function getOnWorkspaceCommand(): WorkspaceCommandHandler | undefined {
  return readConfig()?.onWorkspaceCommand;
}

export const emitWorkspaceCommand = Effect.fn("Embedding.emitWorkspaceCommand")(function* (
  command: string,
  payload: Record<string, unknown>,
) {
  const handler = getOnWorkspaceCommand();
  if (!handler) {
    return { ok: true };
  }
  const result = yield* Effect.tryPromise({
    try: () => Promise.resolve(handler(command, payload)),
    catch: (cause) => EmbeddingCallbackError.make({ operation: `run workspace command ${command}`, cause }),
  }).pipe(
    Effect.catchTag("EmbeddingCallbackError", (failure) =>
      Effect.sync(() => {
        const message = failure.cause instanceof Error ? failure.cause.message : String(failure.cause);
        console.error(`[kenn-forge] workspace command "${command}" failed:`, failure.cause);
        return { ok: false, message };
      }),
    ),
  );
  return yield* decodeCommandResult(`workspace command ${command}`, result);
});

export function initWorkspaceBridge(): void {
  window.__kenn_forge_update_workspace = (data: WorkspaceData) => {
    const config = window.__kenn_forge_config;
    if (config) {
      config.workspace = data;
      window.__kenn_forge_notify_config_changed?.();
    }
  };
  window.__kenn_forge_update_selection = (selection: { hostKey?: string | null; worktreeKey?: string | null }) => {
    const config = window.__kenn_forge_config;
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
    window.__kenn_forge_notify_config_changed?.();
  };
  window.__kenn_forge_update_host_state = (
    hostKey: string,
    patch: {
      connectionState?: WorkspaceHost["connectionState"];
      resources?: WorkspaceResources | null;
    },
  ) => {
    const config = window.__kenn_forge_config;
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
    window.__kenn_forge_notify_config_changed?.();
  };
  window.__kenn_forge_update_tooling = (tooling: ToolingStatus) => {
    const config = window.__kenn_forge_config;
    if (!config) return;
    const embed = { ...(config.embed ?? {}), tooling };
    config.embed = embed;
    window.__kenn_forge_notify_config_changed?.();
  };
}
