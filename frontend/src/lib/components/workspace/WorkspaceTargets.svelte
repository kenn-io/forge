<script lang="ts">
  import { Checkbox, IconButton } from "@kenn-io/kit-ui";
  import GitPullRequestIcon from "@lucide/svelte/icons/git-pull-request";
  import CircleDotIcon from "@lucide/svelte/icons/circle-dot";
  import GroupedSidebarSection from "../shared/GroupedSidebarSection.svelte";
  import XIcon from "@lucide/svelte/icons/x";
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import type { WorkspaceTarget } from "../../api/generated/models/workspaceTarget.js";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import { updateWorkspaceTarget } from "../../api/workspace-targets.js";

  let { workspaceID, workspaceHostKey, refreshToken = 0, disabled = false, onselect }: {
    workspaceID: string;
    workspaceHostKey?: string | undefined;
    refreshToken?: number;
    disabled?: boolean;
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
  const visibleTargets = $derived(targets.filter((target) =>
    showClosed || target.unavailable || !["closed", "merged"].includes(target.state.toLowerCase()),
  ));

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
        <li>
          <button
            class="target-select"
            type="button"
            disabled={disabled || target.unavailable || (target.type === "kata" && (!kataAvailable || !!workspaceHostKey))}
            onclick={() => onselect(target)}
            title={target.title}
          >
            <span class="target-icon" class:target-icon--open={!target.unavailable && target.state === "open"}
              class:target-icon--merged={!target.unavailable && target.state === "merged"}
              class:target-icon--closed={!target.unavailable && target.state === "closed"} aria-hidden="true">
              {#if target.type === "pr"}<GitPullRequestIcon size={14} />{:else}<CircleDotIcon size={14} />{/if}
            </span>
            <span class="target-content">
              <span class="target-title">{target.title || target.url || "Linked task"}</span>
              <span class="target-meta">
                <span class="target-identity">{identity}</span>
                <span class="target-repo">{target.repo?.repo_path ?? target.kata?.daemon_id}</span>
                <span class="target-state">{state}</span>
              </span>
            </span>
          </button>
          {#if target.repo && (target.type === "pr" || target.type === "issue")}
            <IconButton size="sm" ariaLabel={`Remove ${target.type === "pr" ? "PR" : "Issue"} #${target.number} from targets`}
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
  ul { list-style: none; padding: 0; margin: 0; }
  li { display: flex; align-items: center; padding-right: var(--space-3); }
  li + li { border-top: 1px solid var(--border-muted); }
  li:has(.target-select:enabled):hover { background: var(--bg-surface-hover); }
  .target-select { display: flex; flex: 1; align-items: flex-start; min-width: 0; gap: var(--space-3); padding: var(--space-3) var(--space-4); border: 0; background: transparent; color: var(--text-primary); text-align: left; cursor: pointer; font: inherit; }
  .target-select:focus-visible { outline: var(--focus-ring); outline-offset: -2px; }
  .target-select:disabled { cursor: default; color: var(--text-muted); }
  .target-icon { display: flex; flex-shrink: 0; margin-top: var(--space-1); color: var(--text-muted); }
  .target-icon--open { color: var(--accent-green); }
  .target-icon--merged { color: var(--accent-purple); }
  .target-icon--closed { color: var(--accent-red); }
  .target-content { display: flex; flex: 1; min-width: 0; flex-direction: column; gap: var(--space-1); }
  .target-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; line-height: 1.4; }
  .target-meta { display: flex; align-items: baseline; gap: var(--space-3); color: var(--text-muted); font-size: var(--font-size-xs); line-height: 1.4; }
  .target-identity, .target-state { flex-shrink: 0; }
  .target-repo { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .target-state { margin-left: auto; }
  p, .empty { padding: var(--space-3) var(--space-4); color: var(--text-muted); font-size: var(--font-size-xs); }
</style>
