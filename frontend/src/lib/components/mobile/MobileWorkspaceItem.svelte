<script lang="ts">
  import { Spinner } from "@kenn-io/kit-ui";
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { ApiProblemError } from "../../api/effect-errors.js";
  import { apiErrorMessage } from "../../api/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { IssueRouteRef, PullRequestRouteRef } from "../../routes.js";
  import IssueListView from "../../views/IssueListView.svelte";
  import PRListView from "../../views/PRListView.svelte";
  import type { WorkspaceDetail } from "../terminal/workspace-detail.js";
  import { loadMobileWorkspaceDetail, mobileWorkspaceLinkedItem } from "./mobile-workspace-detail.js";
  import { loadMobileWorkspaceSession } from "./mobile-workspace-session.js";

  interface Props {
    workspaceId: string;
    hostKey?: string | undefined;
    tab?: "files" | undefined;
    onBack: () => void;
    onTabChange: (tab: "conversation" | "files", options?: { replace?: boolean }) => void;
  }

  let { workspaceId, hostKey = undefined, tab = undefined, onBack, onTabChange }: Props = $props();
  const appRuntime = getAppRuntime();

  let workspace = $state.raw<WorkspaceDetail | null>(null);
  let loadError = $state<string | null>(null);
  const linkedItem = $derived(workspace ? mobileWorkspaceLinkedItem(workspace) : null);
  const sessionLabel = $derived(loadMobileWorkspaceSession(workspaceId, hostKey));

  const itemRef = $derived.by((): PullRequestRouteRef | IssueRouteRef | null => {
    if (!workspace || !linkedItem) return null;
    return {
      provider: workspace.repo.provider,
      platformHost: workspace.repo.platform_host,
      owner: workspace.repo.owner,
      name: workspace.repo.name,
      repoPath: workspace.repo.repo_path,
      number: linkedItem.number,
    };
  });

  function failureMessage(failure: unknown): string {
    if (failure instanceof ApiProblemError) {
      return apiErrorMessage(failure.problem, hostKey ? "Fleet workspace unavailable" : "Workspace unavailable");
    }
    return failure instanceof Error ? failure.message : "Workspace unavailable";
  }

  function loadWorkspace() {
    return loadMobileWorkspaceDetail(workspaceId, hostKey).pipe(
      Effect.tap((detail) =>
        Effect.sync(() => {
          workspace = detail;
          loadError = null;
        }),
      ),
      Effect.catch((failure) =>
        Effect.sync(() => {
          workspace = null;
          loadError = failureMessage(failure);
        }),
      ),
    );
  }

  function retry(): void {
    loadError = null;
    appRuntime.runCommand(loadWorkspace(), {
      operation: "retry mobile workspace item",
      safeContext: { workspaceId, remote: Boolean(hostKey) },
      onFailure: () => undefined,
    });
  }

  $effect(() => {
    const activeWorkspaceId = workspaceId;
    const activeHostKey = hostKey;
    workspace = null;
    loadError = null;
    const execution = untrack(() =>
      appRuntime.runCommand(loadWorkspace(), {
        operation: "load mobile workspace item",
        safeContext: { workspaceId: activeWorkspaceId, remote: Boolean(activeHostKey) },
        onFailure: (failure) => {
          loadError = failureMessage(failure);
        },
      }),
    );
    return () => execution.interrupt();
  });
</script>

<section class="mobile-workspace-item" aria-label="Workspace linked item">
  <header class="mobile-workspace-item__toolbar">
    <button type="button" class="mobile-workspace-item__back" aria-label="Back to workspace terminal" onclick={onBack}>
      <ArrowLeftIcon size="20" strokeWidth="2" aria-hidden="true" />
      <span>
        <strong>Terminal</strong>
        {#if sessionLabel}<small>{sessionLabel}</small>{/if}
      </span>
    </button>
    {#if linkedItem}
      <span class:issue={linkedItem.itemType === "issue"} class="mobile-workspace-item__badge">
        {linkedItem.itemType === "pr" ? "PR" : "Issue"} #{linkedItem.number}
      </span>
    {/if}
  </header>

  {#if loadError}
    <div class="mobile-workspace-item__state error">
      <strong>Linked item unavailable</strong>
      <span>{loadError}</span>
      <button type="button" onclick={retry}><RefreshCwIcon size="18" aria-hidden="true" />Retry</button>
    </div>
  {:else if !workspace}
    <div class="mobile-workspace-item__state"><Spinner size={18} /><span>Loading linked item…</span></div>
  {:else if !linkedItem || !itemRef}
    <div class="mobile-workspace-item__state">
      <strong>No linked PR or issue</strong>
      <span>This workspace is not associated with a provider item.</span>
      <button type="button" onclick={onBack}>Return to terminal</button>
    </div>
  {:else}
    <div class="mobile-workspace-item__content">
      {#if linkedItem.itemType === "pr"}
        <PRListView
          selectedPR={itemRef}
          detailTab={tab === "files" ? "files" : "conversation"}
          isSidebarCollapsed={true}
          hideSidebar={true}
          autoSyncDetail="background"
          hideStaleDetailWhileLoading={true}
          onDetailTabChange={onTabChange}
        />
      {:else}
        <IssueListView
          selectedIssue={itemRef}
          isSidebarCollapsed={true}
          hideSidebar={true}
          autoSyncDetail="background"
          hideStaleDetailWhileLoading={true}
        />
      {/if}
    </div>
  {/if}
</section>

<style>
  .mobile-workspace-item { flex: 1; min-height: 0; display: flex; flex-direction: column; background: var(--bg-primary); }
  .mobile-workspace-item__toolbar { min-height: 3.5rem; display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; padding: 0.375rem 0.625rem; border-bottom: thin solid var(--border-default); background: var(--bg-surface); }
  .mobile-workspace-item__back { min-width: 0; min-height: 2.75rem; display: inline-flex; align-items: center; gap: 0.5rem; padding: 0 0.625rem; border: 0; border-radius: var(--radius-md); color: var(--text-secondary); background: transparent; font: inherit; text-align: left; }
  .mobile-workspace-item__back span { min-width: 0; display: flex; flex-direction: column; }
  .mobile-workspace-item__back strong, .mobile-workspace-item__back small { max-width: 12rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mobile-workspace-item__back strong { color: var(--text-primary); font-size: var(--font-size-md); }
  .mobile-workspace-item__back small { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--font-size-sm); }
  .mobile-workspace-item__badge { flex: 0 0 auto; padding: 0.25rem 0.625rem; border-radius: 999px; color: var(--text-on-accent); background: var(--accent-green); font-family: var(--font-mono); font-size: var(--font-size-sm); font-weight: 700; }
  .mobile-workspace-item__badge.issue { background: var(--accent-amber); }
  .mobile-workspace-item__back:focus-visible, .mobile-workspace-item__state button:focus-visible { outline: 2px solid var(--accent-blue); outline-offset: 2px; }
  .mobile-workspace-item__content { flex: 1; min-height: 0; display: flex; }
  .mobile-workspace-item__content :global(.collapsible-sidebar) { flex: 1; min-width: 0; }
  .mobile-workspace-item__state { flex: 1; min-height: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 0.75rem; padding: 2rem 1rem; color: var(--text-muted); text-align: center; font-size: var(--font-size-md); }
  .mobile-workspace-item__state strong { color: var(--text-primary); font-size: var(--font-size-xl); }
  .mobile-workspace-item__state.error span { color: var(--accent-red); }
  .mobile-workspace-item__state button { min-height: 2.75rem; display: inline-flex; align-items: center; gap: 0.5rem; padding: 0 1rem; border: thin solid var(--border-default); border-radius: var(--radius-md); color: var(--text-primary); background: var(--bg-surface); font: inherit; font-weight: 650; }
</style>
