<script lang="ts" module>
  import type { NumberedRouteItemRef } from "../../routes.js";

  export type CurrentWorkspaceTarget =
    | { type: "pr" | "issue"; ref: NumberedRouteItemRef }
    | { type: "kata"; daemonID: string; issueUID: string };
</script>

<script lang="ts">
  import { Checkbox, IconButton } from "@kenn-io/kit-ui";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import GitPullRequestClosedIcon from "@lucide/svelte/icons/git-pull-request-closed";
  import GitMergeIcon from "@lucide/svelte/icons/git-merge";
  import CircleDotIcon from "@lucide/svelte/icons/circle-dot";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import GroupedSidebarSection from "../shared/GroupedSidebarSection.svelte";
  import XIcon from "@lucide/svelte/icons/x";
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import type { WorkspaceTarget } from "../../api/generated/models/workspaceTarget.js";
  import { repositoryKeyFromWire, sameRepositoryKey } from "../../api/repository-key.js";
  import { updateWorkspaceTarget } from "../../api/workspace-targets.js";

  let { workspaceID, workspaceHostKey, refreshToken = 0, disabled = false, current = null, onselect }: {
    workspaceID: string;
    workspaceHostKey?: string | undefined;
    refreshToken?: number;
    disabled?: boolean;
    current?: CurrentWorkspaceTarget | null;
    onselect: (target: WorkspaceTarget) => void;
  } = $props();
  const runtime = getAppRuntime();
  let targets = $state<WorkspaceTarget[]>([]);
  let kataAvailable = $state(false);
  let error = $state(false);
  let showClosed = $state(false);
  let collapsed = $state(true);
  let removing = $state<WorkspaceTarget | null>(null);
  let removeError = $state(false);
  let removalRevision = $state(0);
  const visibleTargets = $derived(targets.filter((target) =>
    showClosed || target.unavailable || !["closed", "merged"].includes(target.state.toLowerCase()),
  ));

  type TargetTone = "open" | "merged" | "closed" | "done" | "muted";

  function targetTone(target: WorkspaceTarget): TargetTone {
    if (target.unavailable) return "muted";
    const state = target.state.toLowerCase();
    if (state === "open") return "open";
    if (state === "merged") return "merged";
    if (state === "closed") return target.type === "issue" ? "done" : "closed";
    return "muted";
  }

  function isCurrent(target: WorkspaceTarget): boolean {
    if (!current) return false;
    if (current.type === "kata") {
      return target.type === "kata" && target.kata?.daemon_id === current.daemonID &&
        target.kata.issue_uid === current.issueUID;
    }
    const { ref } = current;
    const repo = target.repo;
    if (target.type !== current.type || target.number !== ref.number || !repo) return false;
    if (repo.provider !== ref.provider) return false;
    if (ref.platformHost && repo.platform_host !== ref.platformHost) return false;
    const key = repositoryKeyFromWire(repo);
    return key && ref.repositoryKey ? sameRepositoryKey(key, ref.repositoryKey) : repo.repo_path === ref.repoPath;
  }

  function removeTarget(target: WorkspaceTarget): void {
    if (!target.repo || (target.type !== "pr" && target.type !== "issue")) return;
    const repo = target.repo;
    removing = target;
    removeError = false;
    runtime.runCommand(updateWorkspaceTarget(workspaceID, workspaceHostKey, target.type, {
      provider: repo.provider, platformHost: repo.platform_host, repositoryKey: repositoryKeyFromWire(repo),
      owner: repo.owner, name: repo.name, repoPath: repo.repo_path, number: target.number,
    }, true).pipe(
      Effect.tap(() => Effect.sync(() => {
        removalRevision++;
        targets = targets.filter((item) => item.type !== target.type || item.number !== target.number ||
          item.repo?.provider !== repo.provider || item.repo?.platform_host !== repo.platform_host ||
          repositoryKeyFromWire(item.repo) !== repositoryKeyFromWire(repo));
      })),
      Effect.ensuring(Effect.sync(() => { removing = null; })),
    ), {
      operation: "remove workspace target", safeContext: { workspaceID },
      onFailure: () => { removeError = true; },
    });
  }

  $effect(() => {
    const id = workspaceID;
    const hostKey = workspaceHostKey;
    void refreshToken;
    void removalRevision;
    const execution = untrack(() => runtime.runCommand(
      executeGeneratedApiRequest("load workspace targets", (client, signal) => {
        if (hostKey?.startsWith("devbox:")) {
          return client.DevboxesService.listDevboxWorkspaceTargets({ connectionId: hostKey.slice(7), id }, { signal });
        }
        if (hostKey) return client.FleetService.listFleetWorkspaceTargets({ hostKey, id }, { signal });
        return client.WorkspacesService.listWorkspaceTargets({ id }, { signal });
      }).pipe(
        Effect.tap((result) => Effect.sync(() => {
          targets = result.targets;
          kataAvailable = result.kata_available;
          error = false;
        })),
        Effect.catch(() => Effect.sync(() => { error = true; })),
      ),
      { operation: "load workspace targets", safeContext: { workspaceID: id }, onFailure: () => {} },
    ));
    return execution.interrupt;
  });
</script>

<div class="workspace-targets">
  <GroupedSidebarSection label="Targets" count={visibleTargets.length} {collapsed} onclick={() => { collapsed = !collapsed; }}>
    {#snippet actions()}
      <Checkbox label="Show closed" bind:checked={showClosed} />
    {/snippet}
    {#if error}<p role="status">Could not refresh targets.</p>{/if}
    {#if removeError}<p role="alert">Could not remove target. Try again.</p>{/if}
    <ul aria-label="Workspace targets">
      {#each visibleTargets as target (`${target.type}:${JSON.stringify(target.repo ?? target.kata)}:${target.number}`)}
        {@const identity = target.type === "kata" ? target.kata?.reference || "Kata task" : `${target.type === "pr" ? "PR" : "Issue"} #${target.number}`}
        {@const state = target.unavailable ? "Unavailable" : target.state}
        {@const tone = targetTone(target)}
        {@const selected = isCurrent(target)}
        <li class:current={selected}>
          <button
            class="target-select"
            type="button"
            disabled={disabled || target.unavailable || (target.type === "kata" && (!kataAvailable || !!workspaceHostKey))}
            aria-current={selected ? "true" : undefined}
            onclick={() => onselect(target)}
            title={target.title}
          >
            <span class={["target-icon", `tone-${tone}`]} aria-hidden="true">
              {#if target.type === "pr"}
                {#if tone === "merged"}<GitMergeIcon size={14} />
                {:else if tone === "closed"}<GitPullRequestClosedIcon size={14} />
                {:else}<GitPullRequestIcon size={14} />{/if}
              {:else if tone === "done"}<CircleCheckIcon size={14} />
              {:else}<CircleDotIcon size={14} />{/if}
            </span>
            <span class="target-content">
              <span class="target-title">{target.title || target.url || "Linked task"}</span>
              <span class="target-meta">
                <span class="target-identity">{identity}</span>
                <span class="target-sep" aria-hidden="true">·</span>
                <span class="target-repo">{target.repo?.repo_path ?? target.kata?.daemon_id}</span>
                {#if tone === "open"}
                  <span class="kit-sr-only">{state}</span>
                {:else if state}
                  <span class="target-sep" aria-hidden="true">·</span>
                  <span class={["target-state", `tone-${tone}`]}>{state}</span>
                {/if}
              </span>
            </span>
          </button>
          {#if target.repo && (target.type === "pr" || target.type === "issue")}
            <IconButton size="sm" class="target-remove"
              ariaLabel={`Remove ${target.type === "pr" ? "PR" : "Issue"} #${target.number} from targets`}
              disabled={disabled || removing !== null} onclick={() => removeTarget(target)}>
              <XIcon size={14} aria-hidden="true" />
            </IconButton>
          {/if}
        </li>
      {:else}
        <li class="empty">{targets.length ? "No open targets." : "No tracked targets yet."}</li>
      {/each}
    </ul>
  </GroupedSidebarSection>
</div>

<style>
  .workspace-targets { flex: 0 0 auto; max-height: 40%; overflow: auto; font-size: var(--font-size-sm); }
  .workspace-targets :global(.kit-checkbox) { gap: var(--space-3); }
  .workspace-targets :global(.kit-checkbox__label) { font-size: inherit; color: inherit; }
  ul { list-style: none; padding: 0; margin: 0; }
  li { display: flex; align-items: center; gap: var(--space-2); padding-right: var(--space-3); }
  li + li { border-top: 1px solid var(--border-muted); }
  li:has(.target-select:enabled):hover { background: var(--sidebar-row-hover-bg, var(--bg-surface-hover)); }
  li.current { background: var(--bg-row-selected); box-shadow: inset 3px 0 0 var(--accent-blue); }
  li.current .target-title { font-weight: 600; }
  .target-select { display: flex; flex: 1; align-items: flex-start; min-width: 0; gap: var(--space-3); padding: var(--space-3) var(--space-2) var(--space-3) var(--space-4); border: 0; background: transparent; color: var(--text-primary); text-align: left; cursor: pointer; font: inherit; line-height: 1.4; }
  .target-select:focus-visible { outline: var(--focus-ring); outline-offset: -2px; }
  .target-select:disabled { cursor: default; color: var(--text-muted); }
  .target-icon { display: flex; flex-shrink: 0; align-items: center; height: 1lh; }
  .tone-open { color: var(--accent-green); }
  .tone-merged, .tone-done { color: var(--accent-purple); }
  .tone-closed { color: var(--accent-red); }
  .tone-muted { color: var(--text-muted); }
  .target-content { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: var(--space-1); }
  .target-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
  .target-meta { display: flex; align-items: baseline; gap: var(--space-2); color: var(--text-muted); font-size: var(--font-size-xs); }
  .target-identity, .target-sep, .target-state { flex-shrink: 0; }
  .target-identity { font-variant-numeric: tabular-nums; }
  .target-repo { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .target-state { font-weight: 500; }
  li :global(.target-remove) { flex-shrink: 0; transition: opacity var(--transition-fast) var(--transition-ease, ease); }
  @media (hover: hover) {
    li:not(:hover, :has(:focus-visible)) :global(.target-remove) { opacity: 0; }
  }
  @media (prefers-reduced-motion: reduce) {
    li :global(.target-remove) { transition: none; }
  }
  p, .empty { margin: 0; padding: var(--space-3) var(--space-4); color: var(--text-muted); font-size: var(--font-size-xs); }
</style>
