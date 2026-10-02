<script lang="ts">
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import type { WorkspaceTarget } from "../../api/generated/models/workspaceTarget.js";

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

<details class="workspace-targets">
  <summary>Targets <span>{targets.length}</span></summary>
  {#if error}
    <p role="status">Could not refresh targets.</p>
  {/if}
  <ul aria-label="Workspace targets">
    {#each targets as target (`${target.type}:${JSON.stringify(target.repo ?? target.kata)}:${target.number}`)}
      <li>
        <button
          type="button"
          disabled={disabled || target.unavailable || (target.type === "kata" && (!kataAvailable || !!workspaceHostKey))}
          onclick={() => onselect(target)}
          title={target.title}
        >
          <span class="target-identity">{target.type === "kata" ? target.kata?.reference || "Kata task" : `${target.type === "pr" ? "PR" : "Issue"} #${target.number}`}<span class="target-state">{target.unavailable ? "Unavailable" : target.state}</span></span>
          <span class="target-title">{target.title || target.url || "Linked task"}</span>
          <span class="target-repo">{target.repo?.repo_path ?? target.kata?.daemon_id} · {target.source}</span>
        </button>
      </li>
    {:else}
      <li class="empty">No tracked targets yet.</li>
    {/each}
  </ul>
</details>

<style>
  .workspace-targets { flex: 0 0 auto; max-height: 40%; overflow: auto; border-bottom: 1px solid var(--border-default); font-size: var(--font-size-sm); }
  summary { cursor: pointer; padding: var(--space-2) var(--space-3); font-weight: 500; }
  summary span { color: var(--text-muted); margin-left: var(--space-1); }
  ul { list-style: none; padding: 0; margin: 0; }
  button { display: flex; flex-direction: column; gap: var(--space-1); width: 100%; padding: var(--space-2) var(--space-3); border: 0; background: transparent; color: var(--text-primary); text-align: left; cursor: pointer; }
  button:hover:not(:disabled) { background: var(--bg-surface-hover); }
  button:focus-visible { outline: var(--focus-ring); outline-offset: -2px; }
  button:disabled { cursor: default; color: var(--text-muted); }
  .target-identity { display: flex; justify-content: space-between; gap: var(--space-2); }
  .target-title { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .target-state, .target-repo { color: var(--text-muted); font-size: var(--font-size-xs); }
  p, .empty { padding: var(--space-2) var(--space-3); color: var(--text-muted); }
</style>
