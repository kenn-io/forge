<script lang="ts">
  // WorkspaceProjectCard renders a single registered project with its
  // worktrees and a primary CTA to create a new one. For the first-
  // useful-minute flow the project is freshly registered and has zero
  // worktrees, so the card opens straight to the New Worktree action.
  // The actions.project["new-worktree"] handler is the embedding
  // host's responsibility to register; we surface failure via the
  // ack-aware runner.

  import { Effect, Option } from "effect";
  import { onDestroy, untrack } from "svelte";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import ProviderIcon from "../provider/ProviderIcon.svelte";
  import {
    getProjectAction,
  } from "../../stores/embed-config.svelte.ts";
  import {
    loadProjectCardSnapshot,
    projectCardFailureMessage,
    type ProjectCardSnapshot,
    type WorkspaceProject,
    type WorkspaceProjectWorktree,
  } from "./project-card-workflow.js";
  import {
    newWorktreeMutationKey,
    ProjectMutationWorkflow,
    projectMutationFailureMessage,
    type NewWorktreeFailure,
  } from "./project-mutation-workflow.js";

  interface Props {
    projectId: string;
    hostKey?: string | null | undefined;
  }

  let { projectId, hostKey = null }: Props = $props();
  const runtime = getAppRuntime();

  let project = $state.raw<WorkspaceProject | null>(null);
  let worktrees = $state.raw<readonly WorkspaceProjectWorktree[]>([]);
  let loadError = $state<string | null>(null);
  let loading = $state<boolean>(true);
  let inFlight = $state<boolean>(false);
  let loadExecution: AppExecution<void, never> | undefined;
  let componentDestroyed = false;
  let reconciliationVersion = 0;
  const scopedHostKey = $derived(hostKey?.trim() || undefined);

  function isCurrentIdentity(targetProjectId: string, targetHostKey?: string): boolean {
    return !componentDestroyed && projectId === targetProjectId && scopedHostKey === targetHostKey;
  }

  function isCurrentReconciliation(
    version: number,
    targetProjectId: string,
    targetHostKey?: string,
  ): boolean {
    return version === reconciliationVersion && isCurrentIdentity(targetProjectId, targetHostKey);
  }

  function applySnapshot(
    snapshot: ProjectCardSnapshot,
    targetProjectId: string,
    targetHostKey?: string,
  ): void {
    if (!isCurrentIdentity(targetProjectId, targetHostKey)) return;
    project = snapshot.project;
    worktrees = snapshot.worktrees;
    loadError = null;
    loading = false;
  }

  function launchLoad(targetProjectId: string, targetHostKey?: string): AppExecution<void, never> {
    const version = ++reconciliationVersion;
    loadExecution?.interrupt();
    loading = true;
    loadError = null;
    const execution = runtime.runCommand(
      loadProjectCardSnapshot(targetProjectId, targetHostKey).pipe(
        Effect.matchEffect({
          onFailure: (failure) => Effect.sync(() => {
            if (!isCurrentIdentity(targetProjectId, targetHostKey)) return;
            loadError = projectCardFailureMessage(failure);
            loading = false;
          }),
          onSuccess: (snapshot) =>
            Effect.gen(function* () {
              yield* Effect.sync(() => applySnapshot(snapshot, targetProjectId, targetHostKey));
              const workflow = yield* ProjectMutationWorkflow;
              const mutationKey = newWorktreeMutationKey(targetProjectId, targetHostKey);
              const retained = yield* workflow.retainedNewWorktree(mutationKey);
              if (Option.isNone(retained) || !isCurrentIdentity(targetProjectId, targetHostKey)) return;
              yield* Effect.sync(() => {
                inFlight = true;
              });
              yield* reconcileNewWorktree(retained.value, mutationKey, version, targetProjectId, targetHostKey);
            }),
        }),
      ),
      {
        operation: "load workspace project card",
        safeContext: {
          projectId: targetProjectId,
          hostKey: targetHostKey ?? "local",
        },
        onFailure: () => {},
      },
    );
    loadExecution = execution;
    return execution;
  }

  $effect(() => {
    const targetProjectId = projectId;
    const targetHostKey = scopedHostKey;
    inFlight = false;
    const execution = untrack(() => launchLoad(targetProjectId, targetHostKey));
    return execution.interrupt;
  });

  onDestroy(() => {
    componentDestroyed = true;
    loadExecution?.interrupt();
  });

  function retryLoad(): void {
    launchLoad(projectId, scopedHostKey);
  }

  function refreshAfterNewWorktree(
    acknowledgement: Effect.Effect<CommandResult, NewWorktreeFailure>,
    mutationKey: string,
    version: number,
    targetProjectId: string,
    targetHostKey?: string,
  ) {
    return Effect.gen(function* () {
      const workflow = yield* ProjectMutationWorkflow;
      yield* Effect.sync(() => {
        if (!isCurrentReconciliation(version, targetProjectId, targetHostKey)) return;
        loading = true;
        loadError = null;
      });
      const reconciled = yield* loadProjectCardSnapshot(targetProjectId, targetHostKey).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              if (!isCurrentReconciliation(version, targetProjectId, targetHostKey)) return false;
              loadError = projectCardFailureMessage(failure);
              loading = false;
              return false;
            }),
          onSuccess: (snapshot) =>
            Effect.sync(() => {
              if (!isCurrentReconciliation(version, targetProjectId, targetHostKey)) return false;
              applySnapshot(snapshot, targetProjectId, targetHostKey);
              return true;
            }),
        }),
      );
      if (reconciled) yield* workflow.forgetNewWorktree(mutationKey, acknowledgement);
    });
  }

  function reconcileNewWorktree(
    acknowledgement: Effect.Effect<CommandResult, NewWorktreeFailure>,
    mutationKey: string,
    version: number,
    targetProjectId: string,
    targetHostKey?: string,
  ) {
    return Effect.gen(function* () {
      const workflow = yield* ProjectMutationWorkflow;
      yield* acknowledgement.pipe(
        Effect.matchEffect({
          onFailure: (failure) => {
            if (!isCurrentIdentity(targetProjectId, targetHostKey)) return Effect.void;
            const reportFailure = Effect.sync(() => {
              if (!isCurrentReconciliation(version, targetProjectId, targetHostKey)) return;
              showFlash(
                projectMutationFailureMessage(
                  failure,
                  "The host returned an invalid worktree acknowledgement.",
                ),
                { tone: "danger" },
              );
            });
            return reportFailure.pipe(
              Effect.andThen(
                refreshAfterNewWorktree(
                  acknowledgement,
                  mutationKey,
                  version,
                  targetProjectId,
                  targetHostKey,
                ),
              ),
            );
          },
          onSuccess: (result) => {
            if (!isCurrentIdentity(targetProjectId, targetHostKey)) return Effect.void;
            if (!result.ok) {
              return Effect.sync(() => {
                if (!isCurrentReconciliation(version, targetProjectId, targetHostKey)) return;
                showFlash(result.message ?? "Couldn't start a new worktree.", {
                  tone: "danger",
                });
              }).pipe(
                Effect.andThen(
                  refreshAfterNewWorktree(
                    acknowledgement,
                    mutationKey,
                    version,
                    targetProjectId,
                    targetHostKey,
                  ),
                ),
              );
            }
            return refreshAfterNewWorktree(
              acknowledgement,
              mutationKey,
              version,
              targetProjectId,
              targetHostKey,
            );
          },
        }),
      );
    }).pipe(
      Effect.ensuring(Effect.sync(() => {
        if (isCurrentReconciliation(version, targetProjectId, targetHostKey)) inFlight = false;
      })),
    );
  }

  function startNewWorktree(): void {
    if (inFlight) return;
    const action = getProjectAction("new-worktree");
    if (!action) {
      showFlash(
        "New Worktree is not available in this build. " +
          "Please update the host application.",
        { tone: "danger" },
      );
      return;
    }
    const targetProjectId = projectId;
    const targetHostKey = scopedHostKey;
    const mutationKey = newWorktreeMutationKey(targetProjectId, targetHostKey);
    const version = ++reconciliationVersion;
    inFlight = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* ProjectMutationWorkflow;
        const outcome = workflow.acceptNewWorktree({
          key: mutationKey,
          action,
          context: {
            surface: "project-card",
            projectId: targetProjectId,
            ...(targetHostKey ? { hostKey: targetHostKey } : {}),
          },
        });
        const acknowledgement = yield* outcome;
        yield* reconcileNewWorktree(acknowledgement, mutationKey, version, targetProjectId, targetHostKey);
      }),
      {
        operation: "start project worktree",
        safeContext: {
          projectId: targetProjectId,
          hostKey: targetHostKey ?? "local",
        },
        onFailure: () => {},
      },
    );
  }

  function platformChip(identity: NonNullable<WorkspaceProject["platform_identity"]>): string {
    return `${identity.platform_host} / ${identity.owner} / ${identity.name}`;
  }
</script>

<section class="project-card" aria-labelledby="project-card-title">
  {#if loading}
    <p class="project-card__status">Loading project…</p>
  {:else if loadError}
    <p class="project-card__error" role="alert">{loadError}</p>
    <button
      type="button"
      class="project-card__retry"
      onclick={retryLoad}
    >
      Retry
    </button>
  {:else if project}
    <header class="project-card__header">
      <h2
        id="project-card-title"
        class="project-card__title"
      >
        {project.display_name}
      </h2>
      <p class="project-card__path">
        <span class="project-card__path-text">{project.local_path}</span>
      </p>
      {#if project.platform_identity}
        <p class="project-card__platform">
          <span class="project-card__platform-chip">
            {#if project.platform_identity.platform}
              <ProviderIcon
                provider={project.platform_identity.platform}
                size={14}
                class="project-card__platform-icon"
              />
            {/if}
            {platformChip(project.platform_identity)}
          </span>
        </p>
      {/if}
      {#if project.default_branch}
        <p class="project-card__branch">
          Default branch:
          <code>{project.default_branch}</code>
        </p>
      {/if}
    </header>

    <section class="project-card__worktrees" aria-label="Worktrees">
      <h3 class="project-card__section-title">Worktrees</h3>
      {#if worktrees.length === 0}
        <p class="project-card__empty">
          This project has no worktrees yet.
        </p>
      {:else}
        <ul class="project-card__worktree-list">
          {#each worktrees as worktree (worktree.id)}
            <li class="project-card__worktree-row">
              <span class="project-card__worktree-branch">
                {worktree.branch}
              </span>
              <span class="project-card__worktree-path">
                {worktree.path}
              </span>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <button
      type="button"
      class="project-card__cta"
      disabled={inFlight}
      aria-busy={inFlight}
      onclick={startNewWorktree}
    >
      {worktrees.length === 0
        ? "Create your first worktree"
        : "Create another worktree"}
      {#if inFlight}
        <span aria-hidden="true">…</span>
      {/if}
    </button>
  {/if}
</section>

<style>
  .project-card {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: 100%;
    max-width: 480px;
    margin: 24px auto;
    padding: 16px;
    box-sizing: border-box;
  }

  .project-card__status {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .project-card__header {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .project-card__title {
    margin: 0;
    font-size: var(--font-size-xl);
    font-weight: 600;
  }

  .project-card__path {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }


  .project-card__path-text {
    font-family: var(--font-mono, monospace);
    word-break: break-all;
  }

  .project-card__platform {
    margin: 0;
  }

  .project-card__platform-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 8px;
    border-radius: 10px;
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-xs);
  }

  :global(.project-card__platform-icon) {
    color: var(--text-secondary);
  }

  .project-card__branch {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .project-card__branch code {
    font-family: var(--font-mono, monospace);
    background: var(--bg-inset);
    padding: 1px 4px;
    border-radius: 4px;
  }

  .project-card__section-title {
    margin: 0 0 4px 0;
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }

  .project-card__empty {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .project-card__worktree-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .project-card__worktree-row {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 8px 10px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md, 8px);
    background: var(--bg-surface);
  }

  .project-card__worktree-branch {
    font-weight: 600;
    font-size: var(--font-size-md);
  }

  .project-card__worktree-path {
    font-family: var(--font-mono, monospace);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    word-break: break-all;
  }

  .project-card__cta {
    appearance: none;
    border: 1px solid var(--accent-blue);
    background: var(--accent-blue);
    color: white;
    font: inherit;
    font-weight: 600;
    padding: 10px 14px;
    border-radius: var(--radius-md, 8px);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
  }

  .project-card__cta:disabled {
    cursor: not-allowed;
    opacity: 0.7;
  }

  .project-card__error {
    margin: 0;
    padding: 8px 12px;
    border: 1px solid var(--accent-red);
    border-radius: var(--radius-md, 8px);
    background: color-mix(in srgb, var(--accent-red) 10%, transparent);
    color: var(--accent-red);
    font-size: var(--font-size-md);
  }

  .project-card__retry {
    appearance: none;
    align-self: flex-start;
    padding: 4px 10px;
    border-radius: var(--radius-md, 8px);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    cursor: pointer;
    font: inherit;
    font-size: var(--font-size-sm);
  }
</style>
