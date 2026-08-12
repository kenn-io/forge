<script lang="ts">
  import { Modal, SelectDropdown, Spinner, type SelectDropdownOption } from "@kenn-io/kit-ui";
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import SquareIcon from "@lucide/svelte/icons/square";
  import { Effect, Option } from "effect";
  import { untrack } from "svelte";
  import type { RuntimeSession } from "../../api/types.js";
  import { apiErrorMessage } from "../../api/runtime.js";
  import { ApiProblemError } from "../../api/effect-errors.js";
  import { workspaceSessionWebSocketPath, type WorkspaceRuntimeState } from "../../api/workspace-runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import {
    noteSessionDiscarded,
    noteSessionMounted,
    noteSessionReleased,
    onSessionExited,
    requestSessionFocus,
    sessionHostKey,
    type SessionHostKey,
  } from "../../stores/session-host.svelte.js";
  import SessionTerminalSlot from "../terminal/SessionTerminalSlot.svelte";
  import type { WorkspaceDetail } from "../terminal/workspace-detail.js";
  import {
    makeWorkspaceRuntimeOwner,
    makeWorkspaceRuntimePresenterID,
    WorkspaceRuntimeWorkflow,
    type WorkspaceRuntimeMutationState,
    type WorkspaceRuntimeTarget,
  } from "../terminal/workspace-runtime-workflow.js";
  import { loadMobileWorkspaceDetail, mobileWorkspaceLinkedItem } from "./mobile-workspace-detail.js";
  import {
    loadMobileWorkspaceSession,
    saveMobileWorkspaceSession,
    selectMobileWorkspaceSession,
  } from "./mobile-workspace-session.js";

  interface Props {
    workspaceId: string;
    hostKey?: string | undefined;
    onBack: () => void;
    onOpenItem: () => void;
  }

  let { workspaceId, hostKey = undefined, onBack, onOpenItem }: Props = $props();
  const appRuntime = getAppRuntime();
  const runtimeOwner = makeWorkspaceRuntimeOwner("mobile-workspace");
  const presenterID = makeWorkspaceRuntimePresenterID();

  let workspace = $state.raw<WorkspaceDetail | null>(null);
  let runtime = $state.raw<WorkspaceRuntimeState | null>(null);
  let selectedSessionKey = $state<string | null>(null);
  let ownedHostKeys: SessionHostKey[] = [];
  let loadError = $state<string | null>(null);
  let runtimeError = $state<string | null>(null);
  let launchingTarget = $state<string | null>(null);
  let stoppingSession = $state<string | null>(null);
  let launchSheetOpen = $state(false);

  const sessions = $derived(runtime?.sessions ?? []);
  const launchTargets = $derived(runtime?.launch_targets ?? []);
  const sessionOptions = $derived<SelectDropdownOption[]>(
    sessions.map((session) => ({ value: session.key, label: session.label })),
  );
  const selectedSession = $derived(
    sessions.find((session) => session.key === selectedSessionKey) ?? null,
  );
  const selectedHostKey = $derived(
    selectedSession ? pooledHostKey(selectedSession) : null,
  );
  const linkedItem = $derived(workspace ? mobileWorkspaceLinkedItem(workspace) : null);

  function target(): WorkspaceRuntimeTarget {
    return { workspaceId, ...(hostKey === undefined ? {} : { hostKey }) };
  }

  function pooledHostKey(session: RuntimeSession): SessionHostKey {
    return sessionHostKey(workspaceId, hostKey, session.key, session.created_at);
  }

  function failureMessage(failure: unknown, fallback: string): string {
    if (failure instanceof ApiProblemError) return apiErrorMessage(failure.problem, fallback);
    return failure instanceof Error ? failure.message : fallback;
  }

  function releaseOwnedSessions(): void {
    for (const key of ownedHostKeys) noteSessionReleased(key);
    ownedHostKeys = [];
  }

  function reconcilePooledSessions(nextSessions: readonly RuntimeSession[]): void {
    const desiredKeys = nextSessions.map(pooledHostKey);
    const desired = new Set(desiredKeys);
    for (const session of nextSessions) {
      const pooledKey = pooledHostKey(session);
      noteSessionMounted({
        hostKey: pooledKey,
        websocketPath: workspaceSessionWebSocketPath(workspaceId, session.key, hostKey),
        status: session.status,
        cursorWheelInput: session.kind === "agent",
      });
    }
    for (const key of ownedHostKeys) {
      if (!desired.has(key)) noteSessionDiscarded(key);
    }
    ownedHostKeys = desiredKeys;
  }

  function applyRuntime(next: WorkspaceRuntimeState): void {
    runtime = next;
    reconcilePooledSessions(next.sessions);
    const preferred =
      selectedSessionKey ?? loadMobileWorkspaceSession(workspaceId, hostKey);
    selectedSessionKey = selectMobileWorkspaceSession(next.sessions, preferred);
    saveMobileWorkspaceSession(workspaceId, hostKey, selectedSessionKey);
    runtimeError = null;
  }

  function requestSessionFocusForSelection(): void {
    const session = runtime?.sessions.find((candidate) => candidate.key === selectedSessionKey);
    if (session) requestSessionFocus(pooledHostKey(session), { soft: true });
  }

  function readRuntime(force = false) {
    return Effect.gen(function* () {
      const workflow = yield* WorkspaceRuntimeWorkflow;
      const result = yield* workflow.read(runtimeOwner, workspaceId, hostKey, { force });
      if (Option.isSome(result)) {
        yield* Effect.sync(() => applyRuntime(result.value));
      }
    }).pipe(
      Effect.catch((failure) =>
        Effect.sync(() => {
          runtimeError = failureMessage(failure, "Runtime unavailable");
        }),
      ),
    );
  }

  function loadWorkspaceAndRuntime() {
    return Effect.gen(function* () {
      const detail = yield* loadMobileWorkspaceDetail(workspaceId, hostKey);
      yield* Effect.sync(() => {
        workspace = detail;
        loadError = null;
      });
      if (detail.status === "ready") yield* readRuntime(true);
    }).pipe(
      Effect.catch((failure) =>
        Effect.sync(() => {
          if (failure instanceof ApiProblemError && failure.problem.status === 404) {
            onBack();
            return;
          }
          loadError = failureMessage(failure, hostKey ? "Fleet workspace unavailable" : "Workspace unavailable");
        }),
      ),
    );
  }

  function observeMutation(state: WorkspaceRuntimeMutationState): Effect.Effect<boolean> {
    return Effect.sync(() => {
      if (state.operation === "Launch") {
        if (state.kind === "pending") launchingTarget = state.request.targetKey;
        else if (state.kind === "succeeded") {
          launchingTarget = null;
          launchSheetOpen = false;
          selectedSessionKey = state.session.key;
          saveMobileWorkspaceSession(workspaceId, hostKey, selectedSessionKey);
          requestRuntimeRefresh();
        } else if (state.kind === "failed" || state.kind === "uncertain") {
          launchingTarget = null;
          showMutationFailure(state, "Launch failed");
        }
      }
      if (state.operation === "Stop") {
        if (state.kind === "pending") stoppingSession = state.request.sessionKey;
        else if (state.kind === "succeeded") {
          stoppingSession = null;
          requestRuntimeRefresh();
        } else if (state.kind === "failed" || state.kind === "uncertain") {
          stoppingSession = null;
          showMutationFailure(state, "Stop failed");
        }
      }
      return true;
    });
  }

  function showMutationFailure(
    state: Extract<WorkspaceRuntimeMutationState, { kind: "failed" | "uncertain" }>,
    fallback: string,
  ): void {
    runtimeError = failureMessage(state.error, fallback);
  }

  function requestRuntimeRefresh(): void {
    appRuntime.runCommand(readRuntime(true), {
      operation: "refresh mobile workspace runtime",
      safeContext: { workspaceId, remote: Boolean(hostKey) },
      onFailure: () => undefined,
    });
  }

  function selectSession(sessionKey: string): void {
    selectedSessionKey = sessionKey;
    saveMobileWorkspaceSession(workspaceId, hostKey, selectedSessionKey);
    requestSessionFocusForSelection();
  }

  function launch(targetKey: string): void {
    if (launchingTarget || stoppingSession) return;
    appRuntime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkspaceRuntimeWorkflow;
        yield* workflow.launch(target(), targetKey, "workflow", { _tag: "Workflow" });
      }),
      {
        operation: "launch mobile workspace session",
        safeContext: { workspaceId, targetKey, remote: Boolean(hostKey) },
        onFailure: (failure) => {
          launchingTarget = null;
          runtimeError = failureMessage(failure, "Launch failed");
        },
      },
    );
  }

  function stopSelectedSession(): void {
    const session = selectedSession;
    if (!session || stoppingSession || launchingTarget) return;
    appRuntime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* WorkspaceRuntimeWorkflow;
        yield* workflow.stop(target(), session.key);
      }),
      {
        operation: "stop mobile workspace session",
        safeContext: { workspaceId, sessionKey: session.key, remote: Boolean(hostKey) },
        onFailure: (failure) => {
          stoppingSession = null;
          runtimeError = failureMessage(failure, "Stop failed");
        },
      },
    );
  }

  $effect(() => {
    const activeWorkspaceId = workspaceId;
    const activeHostKey = hostKey;
    const activeTarget: WorkspaceRuntimeTarget = {
      workspaceId: activeWorkspaceId,
      ...(activeHostKey === undefined ? {} : { hostKey: activeHostKey }),
    };
    workspace = null;
    runtime = null;
    selectedSessionKey = loadMobileWorkspaceSession(activeWorkspaceId, activeHostKey);
    loadError = null;
    runtimeError = null;
    releaseOwnedSessions();

    const execution = untrack(() =>
      appRuntime.runCommand(
        Effect.scoped(
          Effect.gen(function* () {
            const workflow = yield* WorkspaceRuntimeWorkflow;
            yield* workflow.claimPresenter(activeTarget, presenterID, observeMutation, {
              presentationIsCurrent: () => workspaceId === activeWorkspaceId && hostKey === activeHostKey,
            });
            yield* Effect.addFinalizer(() =>
              workflow.releasePresenter(activeTarget, presenterID).pipe(
                Effect.andThen(workflow.release(runtimeOwner)),
              ),
            );
            yield* loadWorkspaceAndRuntime();
            yield* Effect.forever(
              Effect.sleep("5 seconds").pipe(Effect.andThen(readRuntime(true))),
            );
          }),
        ),
        {
          operation: "run mobile workspace terminal",
          safeContext: { workspaceId: activeWorkspaceId, remote: Boolean(activeHostKey) },
          onFailure: (failure) => {
            loadError = failureMessage(failure, "Workspace unavailable");
          },
        },
      ),
    );

    const stopExitListener = onSessionExited((exitedHostKey) => {
      const current = runtime;
      if (!current || !ownedHostKeys.includes(exitedHostKey)) return;
      const nextSessions = current.sessions.filter((session) => pooledHostKey(session) !== exitedHostKey);
      applyRuntime({ ...current, sessions: nextSessions });
      requestRuntimeRefresh();
    });

    return () => {
      stopExitListener();
      execution.interrupt();
      releaseOwnedSessions();
    };
  });
</script>

<section class="mobile-workspace-terminal" aria-label="Workspace terminal">
  <header class="mobile-workspace-terminal__toolbar">
    <button type="button" class="mobile-workspace-terminal__back" aria-label="Back to workspaces" onclick={onBack}>
      <ArrowLeftIcon size="20" strokeWidth="2" aria-hidden="true" />
    </button>
    {#if sessions.length > 0}
      <div class="mobile-workspace-terminal__switcher">
        <SelectDropdown
          title="Terminal session"
          value={selectedSessionKey ?? ""}
          options={sessionOptions}
          onchange={selectSession}
        />
      </div>
    {:else}
      <div class="mobile-workspace-terminal__identity">
        <strong>{workspace ? workspace.git_head_ref : "Workspace"}</strong>
        {#if hostKey}<small>{hostKey}</small>{/if}
      </div>
    {/if}
    <div class="mobile-workspace-terminal__actions">
      {#if linkedItem}
        <button type="button" class="mobile-workspace-terminal__item" aria-label={`Open linked ${linkedItem.itemType === "pr" ? "PR" : "issue"} #${linkedItem.number}`} onclick={onOpenItem}>#{linkedItem.number}</button>
      {/if}
      <button type="button" aria-label="Launch session" onclick={() => (launchSheetOpen = true)} disabled={!workspace || workspace.status !== "ready"}>
        <PlusIcon size="20" strokeWidth="2" aria-hidden="true" />
      </button>
      {#if selectedSession}
        <button type="button" aria-label={`Stop terminal ${selectedSession.label}`} disabled={stoppingSession !== null || launchingTarget !== null} onclick={stopSelectedSession}>
          {#if stoppingSession === selectedSession.key}<Spinner size={16} />{:else}<SquareIcon size="17" strokeWidth="2" aria-hidden="true" />{/if}
        </button>
      {/if}
    </div>
  </header>

  {#if loadError}
    <div class="mobile-workspace-terminal__state error">
      <strong>{hostKey ? `Fleet · ${hostKey}` : "Workspace unavailable"}</strong>
      <span>{loadError}</span>
      <button type="button" onclick={() => {
        loadError = null;
        appRuntime.runCommand(loadWorkspaceAndRuntime(), {
          operation: "retry mobile workspace",
          safeContext: { workspaceId, remote: Boolean(hostKey) },
          onFailure: () => undefined,
        });
      }}><RefreshCwIcon size="18" aria-hidden="true" />Retry</button>
    </div>
  {:else if !workspace}
    <div class="mobile-workspace-terminal__state"><Spinner size={18} /><span>Loading workspace…</span></div>
  {:else if workspace.status !== "ready"}
    <div class="mobile-workspace-terminal__state">
      <strong>{workspace.status === "creating" ? "Setting up workspace…" : "Workspace setup failed"}</strong>
      {#if workspace.error_message}<span>{workspace.error_message}</span>{/if}
    </div>
  {:else if !runtime && !runtimeError}
    <div class="mobile-workspace-terminal__state"><Spinner size={18} /><span>Loading terminal sessions…</span></div>
  {:else if runtimeError && !runtime}
    <div class="mobile-workspace-terminal__state error">
      <strong>Terminal runtime unavailable</strong><span>{runtimeError}</span>
      <button type="button" onclick={requestRuntimeRefresh}><RefreshCwIcon size="18" aria-hidden="true" />Reconnect</button>
    </div>
  {:else if selectedHostKey}
    <div class="mobile-workspace-terminal__stage" aria-label={`Selected terminal: ${selectedSession?.label ?? "Terminal"}`}>
      <SessionTerminalSlot hostKey={selectedHostKey} visible />
    </div>
  {:else}
    <div class="mobile-workspace-terminal__empty">
      <div><strong>No terminal sessions</strong><span>Launch an agent or shell in this workspace.</span></div>
      <div class="mobile-workspace-terminal__launch-grid">
        {#each launchTargets.filter((target) => target.available) as target (target.key)}
          <button type="button" disabled={launchingTarget !== null} onclick={() => launch(target.key)}>
            {#if launchingTarget === target.key}<Spinner size={16} />{/if}
            {target.kind === "plain_shell" ? "Shell" : target.label}
          </button>
        {/each}
      </div>
    </div>
  {/if}
</section>

{#if launchSheetOpen}
  <Modal
    title="Launch session"
    ariaLabel="Launch workspace session"
    closeLabel="Close launch session"
    width="min(100%, 38rem)"
    maxWidth="100%"
    onclose={() => (launchSheetOpen = false)}
  >
    <div class="mobile-terminal-sheet">
      <small class="mobile-terminal-sheet__branch">{workspace?.git_head_ref ?? workspaceId}</small>
      <div class="mobile-terminal-sheet__targets">
        {#each launchTargets as target (target.key)}
          <button type="button" disabled={!target.available || launchingTarget !== null} title={target.disabled_reason} onclick={() => launch(target.key)}>
            <span><strong>{target.kind === "plain_shell" ? "Shell" : target.label}</strong><small>{target.available ? target.source : target.disabled_reason}</small></span>
            {#if launchingTarget === target.key}<Spinner size={16} />{:else}<PlusIcon size="18" aria-hidden="true" />{/if}
          </button>
        {/each}
        {#if launchTargets.length === 0}<p>No launch targets are configured for this workspace.</p>{/if}
      </div>
    </div>
  </Modal>
{/if}

<style>
  .mobile-workspace-terminal { flex: 1; min-height: 0; display: flex; flex-direction: column; background: var(--bg-primary); }
  .mobile-workspace-terminal__toolbar { min-height: 3.5rem; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 0.5rem; padding: 0.375rem 0.5rem; border-bottom: thin solid var(--border-default); background: var(--bg-surface); }
  .mobile-workspace-terminal__toolbar button { min-width: 2.75rem; min-height: 2.75rem; display: inline-flex; align-items: center; justify-content: center; padding: 0 0.625rem; border: thin solid var(--border-default); border-radius: var(--radius-md); color: var(--text-secondary); background: var(--bg-inset); font: inherit; }
  .mobile-workspace-terminal__toolbar button:focus-visible, .mobile-workspace-terminal__empty button:focus-visible, .mobile-terminal-sheet button:focus-visible { outline: 2px solid var(--accent-blue); outline-offset: 2px; }
  .mobile-workspace-terminal__back { padding: 0 !important; }
  .mobile-workspace-terminal__switcher { min-width: 0; position: relative; }
  .mobile-workspace-terminal__switcher :global(.kit-select-dropdown) { width: 100%; min-width: 0; }
  .mobile-workspace-terminal__switcher :global(.kit-select-dropdown__trigger) { min-height: 2.75rem; padding: 0 2rem 0 0.75rem; border-color: var(--border-default); border-radius: var(--radius-md); color: var(--text-primary); font-size: var(--font-size-md); font-weight: 650; }
  .mobile-workspace-terminal__switcher :global(.kit-select-dropdown__option) { min-height: 2.75rem; font-size: var(--font-size-md); }
  .mobile-workspace-terminal__identity { min-width: 0; display: flex; flex-direction: column; }
  .mobile-workspace-terminal__identity strong, .mobile-workspace-terminal__identity small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mobile-workspace-terminal__identity strong { color: var(--text-primary); font-size: var(--font-size-md); }
  .mobile-workspace-terminal__identity small { color: var(--accent-blue); font-size: var(--font-size-sm); }
  .mobile-workspace-terminal__actions { display: flex; gap: 0.375rem; }
  .mobile-workspace-terminal__item { min-width: auto !important; min-height: 2rem !important; color: var(--text-on-accent) !important; border-color: var(--accent-green) !important; background: var(--accent-green) !important; font-family: var(--font-mono) !important; font-weight: 700 !important; }
  .mobile-workspace-terminal__stage { flex: 1; min-height: 0; display: flex; background: var(--terminal-bg, var(--bg-primary)); }
  .mobile-workspace-terminal__state, .mobile-workspace-terminal__empty { flex: 1; min-height: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 0.75rem; padding: 2rem 1rem; color: var(--text-muted); text-align: center; font-size: var(--font-size-md); }
  .mobile-workspace-terminal__state strong, .mobile-workspace-terminal__empty strong { color: var(--text-primary); font-size: var(--font-size-xl); }
  .mobile-workspace-terminal__state button { min-height: 2.75rem; display: inline-flex; align-items: center; gap: 0.5rem; padding: 0 1rem; border: thin solid var(--border-default); border-radius: var(--radius-md); color: var(--text-primary); background: var(--bg-surface); font: inherit; font-weight: 650; }
  .mobile-workspace-terminal__state.error span { max-width: 32rem; color: var(--accent-red); }
  .mobile-workspace-terminal__empty > div:first-child { display: flex; flex-direction: column; gap: 0.375rem; }
  .mobile-workspace-terminal__launch-grid { width: min(100%, 28rem); display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.625rem; }
  .mobile-workspace-terminal__launch-grid button { min-height: 3rem; display: inline-flex; align-items: center; justify-content: center; gap: 0.5rem; padding: 0 0.75rem; border: thin solid var(--border-default); border-radius: var(--radius-md); color: var(--text-primary); background: var(--bg-surface); font: inherit; font-weight: 650; }
  :global(.kit-modal-overlay:has(.mobile-terminal-sheet)) { align-items: flex-end; }
  :global(.kit-modal-panel:has(.mobile-terminal-sheet)) { max-height: 78vh; border-bottom: 0; border-radius: var(--radius-lg) var(--radius-lg) 0 0; }
  :global(.kit-modal-body:has(> .mobile-terminal-sheet)) { padding: 0 0 max(1rem, env(safe-area-inset-bottom)); }
  .mobile-terminal-sheet__branch { display: block; overflow: hidden; padding: 0.625rem 0.875rem; color: var(--text-muted); border-bottom: thin solid var(--border-muted); font-family: var(--font-mono); font-size: var(--font-size-sm); text-overflow: ellipsis; white-space: nowrap; }
  .mobile-terminal-sheet__targets { display: grid; padding: 0 0.875rem; }
  .mobile-terminal-sheet__targets > button { min-height: 4rem; display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 0.5rem 0; border: 0; border-bottom: thin solid var(--border-muted); color: var(--text-primary); background: transparent; font: inherit; text-align: left; }
  .mobile-terminal-sheet__targets > button span { min-width: 0; display: flex; flex-direction: column; gap: 0.125rem; }
  .mobile-terminal-sheet__targets small { color: var(--text-muted); font-size: var(--font-size-sm); }
  .mobile-terminal-sheet__targets p { color: var(--text-muted); font-size: var(--font-size-md); }
</style>
