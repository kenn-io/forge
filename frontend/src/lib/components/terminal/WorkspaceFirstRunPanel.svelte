<script lang="ts">
  // WorkspaceFirstRunPanel owns project registration for both the
  // empty-registry first run and explicit add-project routes. Project-card
  // actions only need to react after a project exists.

  import {
    listUserRepositories,
    projectIntakeFailureMessage,
    type UserRepository,
  } from "../../api/project-intake.ts";
  import {
    loadSnapshotHosts,
    type HostSummary,
  } from "../../api/fleet-snapshot.ts";
  import { SelectDropdown } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import type { AppExecution } from "../../app/runtime.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import {
    getWorkspaceData,
  } from "../../stores/embed-config.svelte.ts";
  import { navigate } from "../../stores/router.svelte.ts";
  import { resolveToolingStatus } from "../../stores/tooling-status.svelte.ts";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import {
    ProjectMutationWorkflow,
    projectMutationFailureMessage,
    projectMutationKey,
    type CloneProjectCommand,
    type RegisterExistingProjectCommand,
  } from "./project-mutation-workflow.js";
  import ToolingStatusBlock from "./ToolingStatusBlock.svelte";

  type ActionId = "add-existing" | "clone" | "connect-github";

  interface Props {
    firstRun?: boolean;
    hostKey?: string | null | undefined;
  }

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

  let { firstRun = true, hostKey = null }: Props = $props();
  const runtime = getAppRuntime();
  let mode = $state<ActionId | null>(null);
  let inFlight = $state(false);
  let repositoryLoadError = $state<string | null>(null);

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
  let snapshotHosts = $state.raw<HostSummary[]>([]);
  let repositoryExecution: AppExecution<void, never> | undefined;
  let componentDestroyed = false;

  const tooling = $derived(resolveToolingStatus(runtime));
  const scopedHostKey = $derived(hostKey?.trim() || undefined);
  const workspaceData = $derived(getWorkspaceData());
  const selectedHost = $derived.by(() => {
    if (snapshotHosts.length > 0) {
      const host = scopedHostKey
        ? snapshotHosts.find(
            (candidate) => candidate.configKey === scopedHostKey,
          )
        : snapshotHosts.find((candidate) => candidate.kind === "self") ??
          snapshotHosts[0];
      if (host) {
        return {
          label: host.name || host.configKey,
          platform: host.platform,
        };
      }
    }
    const workspace = workspaceData;
    if (!workspace) return undefined;
    if (scopedHostKey) {
      return workspace.hosts.find(
        (candidate) => candidate.key === scopedHostKey,
      );
    }
    return workspace.hosts.find(
      (candidate) => candidate.key === workspace.selectedHostKey,
    ) ?? workspace.hosts[0];
  });
  const actionDefinitions = $derived(
    ACTIONS.map((action) => {
      if (action.id !== "add-existing") return action;
      return {
        ...action,
        label: scopedHostKey
          ? "Add an existing repository"
          : "Add an existing local repository",
        description: scopedHostKey
          ? "Register a checkout that already exists on this host."
          : "Register a checkout that already exists here.",
      };
    }),
  );
  const title = $derived(
    firstRun ? "Get to your first worktree." : "Add a project.",
  );
  const lede = $derived(
    firstRun
      ? "Worktrees keep one branch checked out per directory so each change you start has its own working tree, terminal, and agent. Pick a starting point below."
      : "Register an existing checkout or clone a repository so it can be used for worktrees.",
  );
  const hostLabel = $derived(
    scopedHostKey
      ? selectedHost?.label ?? scopedHostKey
      : null,
  );
  const provider = $derived.by(() => {
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
  // Resolve against the filtered list with the same fallback SelectDropdown
  // uses to render (selected value, else first option). This keeps the cloned
  // repo identical to the one shown: if a filter hides the current selection,
  // both the dropdown and submit fall back to the first visible repo instead of
  // cloning a stale, no-longer-visible selection.
  const chosenGithubRepo = $derived(
    filteredRepos.find((repo) => repo.name_with_owner === selectedGithubRepo) ??
      filteredRepos[0],
  );
  const githubRepoOptions = $derived(
    filteredRepos.map((repo) => ({
      value: repo.name_with_owner,
      label: repo.name_with_owner,
    })),
  );

  function isDisabled(definition: ActionDefinition): boolean {
    if (inFlight) return true;
    if (definition.id === "connect-github" && scopedHostKey) return true;
    if (definition.requiresGh && !ghAuthed) return true;
    return false;
  }

  function disabledReason(
    definition: ActionDefinition,
  ): string | undefined {
    if (definition.id === "connect-github" && scopedHostKey) {
      return "Pick from GitHub is only available for the local host. Use Clone a repository for this host.";
    }
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
    repositoryLoadError = null;
    if (definition.id === "connect-github") {
      loadGitHubRepositories();
    }
  }

  $effect(() => {
    const targetHostKey = scopedHostKey;
    repositoryExecution?.interrupt();
    repositoryExecution = undefined;
    githubLoading = false;
    inFlight = false;
    mode = null;
    repositoryLoadError = null;
    existingPath = "";
    cloneURL = "";
    clonePath = "";
    cloneBranch = "";
    githubRepos = [];
    githubFilter = "";
    selectedGithubRepo = "";
    githubPath = "";
    githubBranch = "";
    const execution = untrack(() => runtime.runCommand(
      loadSnapshotHosts().pipe(
        Effect.matchEffect({
          onFailure: () => Effect.sync(() => {
            snapshotHosts = [];
          }),
          onSuccess: (hosts) => Effect.sync(() => {
            snapshotHosts = hosts;
          }),
        }),
      ),
      {
        operation: "load project intake hosts",
        safeContext: { hostKey: targetHostKey ?? "local" },
        onFailure: () => {},
      },
    ));
    return execution.interrupt;
  });

  onDestroy(() => {
    componentDestroyed = true;
    repositoryExecution?.interrupt();
  });

  function backToActions(): void {
    repositoryExecution?.interrupt();
    repositoryExecution = undefined;
    mode = null;
    repositoryLoadError = null;
    githubLoading = false;
    inFlight = false;
  }

  function loadGitHubRepositories(): void {
    repositoryExecution?.interrupt();
    githubLoading = true;
    repositoryLoadError = null;
    repositoryExecution = runtime.runCommand(
      listUserRepositories().pipe(
        Effect.matchEffect({
          onFailure: (failure) => Effect.sync(() => {
            repositoryLoadError = projectIntakeFailureMessage(failure);
            githubLoading = false;
          }),
          onSuccess: (repositories) => Effect.sync(() => {
            githubRepos = repositories;
            selectedGithubRepo = repositories[0]?.name_with_owner ?? "";
            githubLoading = false;
          }),
        }),
      ),
      {
        operation: "load project intake repositories",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  function runProjectSubmission(
    command:
      | ({ readonly _tag: "RegisterExisting" } & RegisterExistingProjectCommand)
      | ({ readonly _tag: "Clone" } & CloneProjectCommand),
    operation: string,
    targetHostKey: string | undefined,
  ): void {
    if (inFlight) return;
    inFlight = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* ProjectMutationWorkflow;
        const acknowledgement = yield* (command._tag === "RegisterExisting"
          ? workflow.acceptRegisterExisting(command)
          : workflow.acceptClone(command));
        yield* acknowledgement.pipe(
          Effect.matchEffect({
            onFailure: (failure) => {
              if (componentDestroyed || scopedHostKey !== targetHostKey) return Effect.void;
              const reportFailure = Effect.sync(() => {
                showFlash(
                  projectMutationFailureMessage(failure, "The host returned an invalid project acknowledgement."),
                  { tone: "danger" },
                );
              });
              return failure._tag === "InvalidEmbeddingAcknowledgement"
                ? reportFailure
                : reportFailure.pipe(Effect.andThen(workflow.forgetProject(command.key)));
            },
            onSuccess: ({ project, acknowledgement: result }) => {
              if (componentDestroyed || scopedHostKey !== targetHostKey) return Effect.void;
              if (!result.ok) {
                return Effect.sync(() => {
                  showFlash(
                    result.message ?? "Project registered, but the host did not refresh.",
                    { tone: "danger" },
                  );
                });
              }
              return Effect.sync(() => {
                if (targetHostKey) {
                  navigate("/workspaces");
                } else {
                  navigate(`/workspaces/embed/project/${encodeURIComponent(project.id)}`);
                }
              }).pipe(Effect.andThen(workflow.forgetProject(command.key)));
            },
          }),
        );
      }).pipe(
        Effect.ensuring(Effect.sync(() => {
          if (!componentDestroyed && scopedHostKey === targetHostKey) inFlight = false;
        })),
      ),
      {
        operation,
        safeContext: { hostKey: targetHostKey ?? "local" },
        onFailure: () => {},
      },
    );
  }

  function submitExisting(): void {
    const targetHostKey = scopedHostKey;
    const path = existingPath;
    runProjectSubmission(
      {
        _tag: "RegisterExisting",
        key: projectMutationKey("register", targetHostKey, [path]),
        path,
        ...(targetHostKey ? { hostKey: targetHostKey } : {}),
      },
      "register existing project",
      targetHostKey,
    );
  }

  function submitClone(): void {
    const targetHostKey = scopedHostKey;
    const url = cloneURL;
    const path = clonePath;
    const branch = cloneBranch;
    runProjectSubmission(
      {
        _tag: "Clone",
        key: projectMutationKey("clone", targetHostKey, [url, path, branch]),
        url,
        path,
        branch,
        ...(targetHostKey ? { hostKey: targetHostKey } : {}),
      },
      "clone project",
      targetHostKey,
    );
  }

  function submitGitHub(): void {
    if (scopedHostKey) {
      showFlash("GitHub repository picking is only available for the local host.", { tone: "danger" });
      return;
    }
    if (inFlight || !chosenGithubRepo) return;
    const repository = chosenGithubRepo;
    if (!repository.ssh_url) {
      showFlash("Selected repository does not expose an SSH clone URL.", {
        tone: "danger",
      });
      return;
    }
    const path = githubPath;
    const branch = githubBranch;
    runProjectSubmission(
      {
        _tag: "Clone",
        key: projectMutationKey("clone", undefined, [repository.ssh_url, path, branch]),
        url: repository.ssh_url,
        path,
        branch,
      },
      "clone selected repository",
      undefined,
    );
  }
</script>

<section class="first-run" aria-labelledby="first-run-title">
  <div class="first-run__intro">
    <h1 id="first-run-title" class="first-run__title">
      {title}
    </h1>
    <p class="first-run__lede">
      {lede}
    </p>
    {#if hostLabel}
      <p class="first-run__host">Host: {hostLabel}</p>
    {/if}
  </div>

  {#if mode === null}
    <ul class="first-run__actions">
      {#each actionDefinitions as action (action.id)}
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
          {actionDefinitions.find((action) => action.id === mode)?.label}
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
            submitExisting();
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
            submitClone();
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
            submitGitHub();
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
              onclick={loadGitHubRepositories}
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
              <SelectDropdown
                title="GitHub repository"
                value={selectedGithubRepo}
                options={githubRepoOptions}
                onchange={(value) => { selectedGithubRepo = value; }}
                disabled={inFlight}
              />
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
                disabled={inFlight || !chosenGithubRepo}
              >
                {inFlight ? "Cloning..." : "Clone repository"}
              </button>
            </div>
          {/if}
        </form>
      {/if}
    </div>
  {/if}

  {#if repositoryLoadError}
    <p class="first-run__error" role="alert">
      {repositoryLoadError}
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

  .first-run__host {
    margin: 0;
    color: var(--text-muted);
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-sm);
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
    gap: var(--space-5);
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

  .first-run-field input {
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

  .first-run-field input:disabled {
    opacity: 0.6;
  }

  .first-run-field :global(.kit-select-dropdown) {
    width: 100%;
    min-width: 0;
  }

  .first-run-field :global(.kit-select-dropdown__trigger) {
    height: 36px;
    font-size: var(--font-size-sm);
    font-weight: 400;
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
