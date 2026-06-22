<script lang="ts">
  // WorkspaceFirstRunPanel renders when the project registry is empty.
  // Project registration is owned here so embedders only need to react
  // after a project exists.

  import {
    cloneProject,
    listUserRepositories,
    registerExistingProject,
    type ProjectResponse,
    type UserRepository,
  } from "../../api/project-intake.ts";
  import {
    emitWorkspaceCommand,
    getWorkspaceData,
  } from "../../stores/embed-config.svelte.ts";
  import { navigate } from "../../stores/router.svelte.ts";
  import { resolveToolingStatus } from "../../stores/tooling-status.svelte.ts";
  import ToolingStatusBlock from "./ToolingStatusBlock.svelte";

  type ActionId = "add-existing" | "clone" | "connect-github";

  interface ActionDefinition {
    id: ActionId;
    label: string;
    description: string;
    requiresGh: boolean;
  }

  const ACTIONS: ActionDefinition[] = [
    {
      id: "add-existing",
      label: "Add an existing local repository",
      description: "Register a checkout that already exists here.",
      requiresGh: false,
    },
    {
      id: "clone",
      label: "Clone a repository",
      description: "Clone from a Git URL into a destination path.",
      requiresGh: false,
    },
    {
      id: "connect-github",
      label: "Connect a GitHub repository",
      description: "Pick from your GitHub repositories.",
      requiresGh: true,
    },
  ];

  let mode = $state<ActionId | null>(null);
  let inFlight = $state(false);
  let lastError = $state<string | null>(null);

  let existingPath = $state("");
  let cloneURL = $state("");
  let clonePath = $state("");
  let cloneBranch = $state("");

  let githubLoading = $state(false);
  let githubRepos = $state<UserRepository[]>([]);
  let githubFilter = $state("");
  let selectedGithubRepo = $state("");
  let githubPath = $state("");
  let githubBranch = $state("");

  const tooling = $derived(resolveToolingStatus());
  const provider = $derived.by(() => {
    const workspace = getWorkspaceData();
    if (!workspace) return undefined;
    const selectedHost = workspace.hosts.find(
      (host) => host.key === workspace.selectedHostKey,
    ) ?? workspace.hosts[0];
    return selectedHost?.platform;
  });
  const ghAuthed = $derived(
    tooling?.gh?.available === true &&
      tooling.gh.authenticated === true,
  );
  const filteredRepos = $derived.by(() => {
    const query = githubFilter.trim().toLowerCase();
    if (!query) return githubRepos;
    return githubRepos.filter((repo) =>
      repo.name_with_owner.toLowerCase().includes(query),
    );
  });
  const chosenGithubRepo = $derived(
    githubRepos.find(
      (repo) => repo.name_with_owner === selectedGithubRepo,
    ),
  );

  function isDisabled(definition: ActionDefinition): boolean {
    if (inFlight) return true;
    if (definition.requiresGh && !ghAuthed) return true;
    return false;
  }

  function disabledReason(
    definition: ActionDefinition,
  ): string | undefined {
    if (definition.requiresGh && !ghAuthed) {
      if (!tooling?.gh?.available) {
        return "Install gh to use this option.";
      }
      return "Run gh auth login to use this option.";
    }
    return undefined;
  }

  function chooseMode(definition: ActionDefinition): void {
    if (isDisabled(definition)) return;
    mode = definition.id;
    lastError = null;
    if (definition.id === "connect-github") {
      void loadGitHubRepositories();
    }
  }

  function backToActions(): void {
    mode = null;
    lastError = null;
    inFlight = false;
  }

  async function loadGitHubRepositories(): Promise<void> {
    githubLoading = true;
    lastError = null;
    try {
      githubRepos = await listUserRepositories();
      selectedGithubRepo = githubRepos[0]?.name_with_owner ?? "";
    } catch (err) {
      lastError = err instanceof Error ? err.message : String(err);
    } finally {
      githubLoading = false;
    }
  }

  async function finishProject(project: ProjectResponse): Promise<void> {
    const result = await emitWorkspaceCommand("project-registered", {
      projectId: project.id,
    });
    if (!result.ok) {
      lastError =
        result.message ?? "Project registered, but the host did not refresh.";
      return;
    }
    navigate(`/workspaces/embed/project/${encodeURIComponent(project.id)}`);
  }

  async function submitExisting(): Promise<void> {
    if (inFlight) return;
    inFlight = true;
    lastError = null;
    try {
      await finishProject(await registerExistingProject(existingPath));
    } catch (err) {
      lastError = err instanceof Error ? err.message : String(err);
    } finally {
      inFlight = false;
    }
  }

  async function submitClone(): Promise<void> {
    if (inFlight) return;
    inFlight = true;
    lastError = null;
    try {
      await finishProject(
        await cloneProject(cloneURL, clonePath, cloneBranch),
      );
    } catch (err) {
      lastError = err instanceof Error ? err.message : String(err);
    } finally {
      inFlight = false;
    }
  }

  async function submitGitHub(): Promise<void> {
    if (inFlight || !chosenGithubRepo) return;
    if (!chosenGithubRepo.ssh_url) {
      lastError = "Selected repository does not expose an SSH clone URL.";
      return;
    }
    inFlight = true;
    lastError = null;
    try {
      await finishProject(
        await cloneProject(
          chosenGithubRepo.ssh_url,
          githubPath,
          githubBranch,
        ),
      );
    } catch (err) {
      lastError = err instanceof Error ? err.message : String(err);
    } finally {
      inFlight = false;
    }
  }
</script>

<section class="first-run" aria-labelledby="first-run-title">
  <div class="first-run__intro">
    <h1 id="first-run-title" class="first-run__title">
      Get to your first worktree.
    </h1>
    <p class="first-run__lede">
      Worktrees keep one branch checked out per directory so each
      change you start has its own working tree, terminal, and
      agent. Pick a starting point below.
    </p>
  </div>

  {#if mode === null}
    <ul class="first-run__actions">
      {#each ACTIONS as action (action.id)}
        {@const disabled = isDisabled(action)}
        {@const reason = disabledReason(action)}
        <li class="first-run-action">
          <button
            type="button"
            class="first-run-action__button"
            {disabled}
            aria-describedby={reason
              ? `first-run-action-reason-${action.id}`
              : undefined}
            onclick={() => chooseMode(action)}
          >
            <span class="first-run-action__label">
              {action.label}
            </span>
            <span class="first-run-action__description">
              {action.description}
            </span>
          </button>
          {#if reason}
            <p
              class="first-run-action__reason"
              id="first-run-action-reason-{action.id}"
            >
              {reason}
            </p>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <div class="first-run-form">
      <div class="first-run-form__header">
        <h2 class="first-run-form__title">
          {ACTIONS.find((action) => action.id === mode)?.label}
        </h2>
        <button
          type="button"
          class="first-run-form__back"
          onclick={backToActions}
          disabled={inFlight}
        >
          Back
        </button>
      </div>

      {#if mode === "add-existing"}
        <form
          class="first-run-form__body"
          onsubmit={(event) => {
            event.preventDefault();
            void submitExisting();
          }}
        >
          <label class="first-run-field">
            <span>Repository path</span>
            <input
              bind:value={existingPath}
              placeholder="/Users/you/code/repo"
              autocomplete="off"
              disabled={inFlight}
            />
          </label>

          <div class="first-run-form__buttons">
            <button type="button" onclick={backToActions} disabled={inFlight}>
              Cancel
            </button>
            <button type="submit" disabled={inFlight}>
              {inFlight ? "Adding..." : "Add repository"}
            </button>
          </div>
        </form>
      {:else if mode === "clone"}
        <form
          class="first-run-form__body"
          onsubmit={(event) => {
            event.preventDefault();
            void submitClone();
          }}
        >
          <label class="first-run-field">
            <span>Repository URL</span>
            <input
              bind:value={cloneURL}
              placeholder="git@github.com:owner/repo.git"
              autocomplete="off"
              disabled={inFlight}
            />
          </label>
          <label class="first-run-field">
            <span>Destination path</span>
            <input
              bind:value={clonePath}
              placeholder="/Users/you/code/repo"
              autocomplete="off"
              disabled={inFlight}
            />
          </label>
          <label class="first-run-field">
            <span>Branch</span>
            <input
              bind:value={cloneBranch}
              placeholder="Optional"
              autocomplete="off"
              disabled={inFlight}
            />
          </label>

          <div class="first-run-form__buttons">
            <button type="button" onclick={backToActions} disabled={inFlight}>
              Cancel
            </button>
            <button type="submit" disabled={inFlight}>
              {inFlight ? "Cloning..." : "Clone repository"}
            </button>
          </div>
        </form>
      {:else if mode === "connect-github"}
        <form
          class="first-run-form__body"
          onsubmit={(event) => {
            event.preventDefault();
            void submitGitHub();
          }}
        >
          {#if githubLoading}
            <p class="first-run-form__status">Loading repositories...</p>
          {:else if githubRepos.length === 0}
            <p class="first-run-form__status">
              No repositories were returned for this GitHub account.
            </p>
            <button
              type="button"
              onclick={() => void loadGitHubRepositories()}
              disabled={inFlight}
            >
              Try again
            </button>
          {:else}
            <label class="first-run-field">
              <span>Filter repositories</span>
              <input
                bind:value={githubFilter}
                placeholder="owner/name"
                autocomplete="off"
                disabled={inFlight}
              />
            </label>
            <label class="first-run-field">
              <span>GitHub repository</span>
              <select
                bind:value={selectedGithubRepo}
                disabled={inFlight}
              >
                {#each filteredRepos as repo (repo.name_with_owner)}
                  <option value={repo.name_with_owner}>
                    {repo.name_with_owner}
                  </option>
                {/each}
              </select>
            </label>
            <label class="first-run-field">
              <span>Destination path</span>
              <input
                bind:value={githubPath}
                placeholder="/Users/you/code/repo"
                autocomplete="off"
                disabled={inFlight}
              />
            </label>
            <label class="first-run-field">
              <span>Branch</span>
              <input
                bind:value={githubBranch}
                placeholder={chosenGithubRepo?.default_branch ?? "Optional"}
                autocomplete="off"
                disabled={inFlight}
              />
            </label>

            <div class="first-run-form__buttons">
              <button
                type="button"
                onclick={backToActions}
                disabled={inFlight}
              >
                Cancel
              </button>
              <button
                type="submit"
                disabled={inFlight || !selectedGithubRepo}
              >
                {inFlight ? "Cloning..." : "Clone repository"}
              </button>
            </div>
          {/if}
        </form>
      {/if}
    </div>
  {/if}

  {#if lastError}
    <p class="first-run__error" role="alert">
      {lastError}
    </p>
  {/if}

  <ToolingStatusBlock {tooling} {provider} />
</section>

<style>
  .first-run {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: 100%;
    max-width: 560px;
    margin: 24px auto;
    padding: 16px;
    box-sizing: border-box;
  }

  .first-run__intro {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .first-run__title {
    margin: 0;
    font-size: var(--font-size-xl);
    font-weight: 600;
    color: var(--text-primary);
  }

  .first-run__lede {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-md);
    line-height: 1.5;
  }

  .first-run__actions {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .first-run-action {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .first-run-action__button {
    appearance: none;
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    text-align: left;
    padding: 12px 14px;
    border-radius: var(--radius-md, 8px);
    font: inherit;
    cursor: pointer;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .first-run-action__button:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .first-run-action__button:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }

  .first-run-action__label {
    font-weight: 600;
  }

  .first-run-action__description,
  .first-run-action__reason,
  .first-run-form__status {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.4;
  }

  .first-run-action__reason {
    margin: 0 0 0 2px;
  }

  .first-run-form {
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md, 8px);
    background: var(--bg-surface);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .first-run-form__header,
  .first-run-form__buttons {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .first-run-form__title {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-weight: 600;
  }

  .first-run-form__body {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .first-run-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .first-run-field input,
  .first-run-field select {
    box-sizing: border-box;
    width: 100%;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm, 4px);
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-weight: 400;
    padding: 8px 10px;
  }

  .first-run-field input:disabled,
  .first-run-field select:disabled {
    opacity: 0.6;
  }

  .first-run-form__buttons {
    justify-content: flex-end;
  }

  .first-run-form__buttons button,
  .first-run-form__back,
  .first-run-form__body > button {
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm, 4px);
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    padding: 7px 10px;
  }

  .first-run-form__buttons button[type="submit"] {
    background: var(--accent-blue);
    border-color: var(--accent-blue);
    color: white;
  }

  .first-run-form__buttons button:disabled,
  .first-run-form__back:disabled,
  .first-run-form__body > button:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }

  .first-run__error {
    margin: 0;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
    line-height: 1.4;
  }
</style>
