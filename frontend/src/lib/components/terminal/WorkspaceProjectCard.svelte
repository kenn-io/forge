<script lang="ts">
  // WorkspaceProjectCard renders a single registered project with its
  // worktrees and a primary CTA to create a new one. For the first-
  // useful-minute flow the project is freshly registered and has zero
  // worktrees, so the card opens straight to the New Worktree action.
  // The actions.project["new-worktree"] handler is the embedding
  // host's responsibility to register; we surface failure via the
  // ack-aware runner.

  import { onMount } from "svelte";

  import { apiErrorMessage, client } from "../../api/runtime.ts";
  import ProviderIcon from "../provider/ProviderIcon.svelte";
  import {
    getProjectAction,
    invokeProjectAction,
  } from "../../stores/embed-config.svelte.ts";

  interface Props {
    projectId: string;
    hostKey?: string | null | undefined;
  }

  interface PlatformIdentity {
    platform?: string;
    platform_host: string;
    owner: string;
    name: string;
  }

  interface Project {
    id: string;
    display_name: string;
    local_path: string;
    platform_identity?: PlatformIdentity;
    default_branch?: string;
  }

  interface Worktree {
    id: string;
    project_id: string;
    branch: string;
    path: string;
  }

  let { projectId, hostKey = null }: Props = $props();

  let project = $state<Project | null>(null);
  let worktrees = $state<Worktree[]>([]);
  let loadError = $state<string | null>(null);
  let loading = $state<boolean>(true);
  let inFlight = $state<boolean>(false);
  let actionError = $state<string | null>(null);
  const scopedHostKey = $derived(hostKey?.trim() || undefined);

  async function load(): Promise<void> {
    loading = true;
    loadError = null;
    try {
      const projectResult = scopedHostKey
        ? await client.GET("/fleet/hosts/{host_key}/projects/{project_id}", {
            params: {
              path: { host_key: scopedHostKey, project_id: projectId },
            },
          })
        : await client.GET("/projects/{project_id}", {
            params: { path: { project_id: projectId } },
          });
      const { data: projectData, error: projectError } = projectResult;
      if (!projectData) {
        loadError = apiErrorMessage(
          projectError,
          "Couldn't load this project.",
        );
        return;
      }
      project = projectData as Project;

      const worktreesResult = scopedHostKey
        ? await client.GET("/fleet/hosts/{host_key}/projects/{project_id}/worktrees", {
            params: {
              path: { host_key: scopedHostKey, project_id: projectId },
            },
          })
        : await client.GET("/projects/{project_id}/worktrees", {
            params: { path: { project_id: projectId } },
          });
      const { data: worktreesData, error: worktreesError } = worktreesResult;
      if (!worktreesData) {
        loadError = apiErrorMessage(
          worktreesError,
          "Couldn't load this project's worktrees.",
        );
        return;
      }
      worktrees = (worktreesData.worktrees ?? []) as Worktree[];
    } catch (err) {
      loadError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
  });

  async function startNewWorktree(): Promise<void> {
    if (inFlight) return;
    const action = getProjectAction("new-worktree");
    if (!action) {
      actionError =
        "New Worktree is not available in this build. " +
        "Please update the host application.";
      return;
    }
    inFlight = true;
    actionError = null;
    try {
      const result = await invokeProjectAction(action, {
        surface: "project-card",
        projectId,
        ...(scopedHostKey ? { hostKey: scopedHostKey } : {}),
      });
      if (!result.ok) {
        actionError = result.message ?? "Couldn't start a new worktree.";
        return;
      }
      // Refresh worktree list on success so the new row shows up.
      await load();
    } finally {
      inFlight = false;
    }
  }

  function platformChip(identity: PlatformIdentity): string {
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
      onclick={() => void load()}
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
      onclick={() => void startNewWorktree()}
    >
      {worktrees.length === 0
        ? "Create your first worktree"
        : "Create another worktree"}
      {#if inFlight}
        <span aria-hidden="true">…</span>
      {/if}
    </button>

    {#if actionError}
      <p class="project-card__error" role="alert">{actionError}</p>
    {/if}
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
