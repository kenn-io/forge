<script lang="ts">
  import { Effect } from "effect";
  import { getStores } from "../../context.js";
  import WorkspaceCreateSplitButton from "../workspace/WorkspaceCreateSplitButton.svelte";
  import { Button, TextInput, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import GitBranchIcon from "@lucide/svelte/icons/git-branch";
  import { canonicalProvider, providerRepoPath, providerRouteParams } from "../../api/provider-routes.js";
  import type { Repo } from "../../api/types.js";
  import {
    ApiProblemError,
    InvalidExternalPayload,
    type TransientTransportError,
  } from "../../api/effect-errors.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import type { ProblemBody } from "../../api/problems.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { queueWorkspaceLaunch } from "../../stores/workspace-create-pending.svelte.js";
  import Modal from "../shared/Modal.svelte";
  import { apiErrorMessage } from "../../api/runtime.js";
  import { navigate } from "../../stores/router.svelte.js";
  import {
    getLastUsedNewWorkspaceRepoKey,
    rememberNewWorkspaceRepoKey,
    type NewWorkspaceRepoSeed,
  } from "../../stores/new-workspace.svelte.js";

  // Starts new work in a tracked repository without a pull request, issue, or
  // Kata task: pick the repo, optionally name the branch, and kenn-forge
  // materializes a worktree from the repository's default branch.

  interface Props {
    open: boolean;
    onClose: () => void;
    seedRepo?: NewWorkspaceRepoSeed | null;
    // Overridable so callers embedding this dialog outside the app shell (and
    // tests) can observe the created workspace instead of navigating.
    onCreated?: ((workspaceId: string) => void) | undefined;
  }

  const { open, onClose, seedRepo = null, onCreated = undefined }: Props = $props();
  const { settings } = getStores();
  const runtime = getAppRuntime();

  type RepoOption = {
    key: string;
    provider: string;
    platformHost: string;
    owner: string;
    name: string;
    label: string;
  };

  let repos = $state<RepoOption[]>([]);
  let reposLoading = $state(false);
  let reposError = $state<string | null>(null);
  let selectedKey = $state("");
  let branch = $state("");
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let suggestedBranch = $state<string | null>(null);
  let pendingLaunchTargetKey = $state<string | null>(null);
  let activeSession: object | null = null;
  let repoLoadExecution: AppExecution<void, ApiProblemError | TransientTransportError> | null = null;

  function repoOption(repo: Repo): RepoOption {
    const provider = canonicalProvider(repo.Platform);
    return {
      key: `${provider}/${repo.PlatformHost}/${repo.Owner}/${repo.Name}`,
      provider,
      platformHost: repo.PlatformHost,
      owner: repo.Owner,
      name: repo.Name,
      label: `${repo.Owner}/${repo.Name}`,
    };
  }

  function seedKey(seed: NewWorkspaceRepoSeed | null): string {
    if (!seed) return "";
    return `${canonicalProvider(seed.provider)}/${seed.platformHost}/${seed.owner}/${seed.name}`;
  }

  // Each open starts a fresh request and fresh form state; a stale response
  // from a previous open must not repopulate the list. The previous list and
  // selection are dropped up front so a reopen cannot submit against the repo
  // picked last time while the new list is still in flight, or if it fails.
  $effect(() => {
    if (!open) {
      activeSession = null;
      repoLoadExecution?.interrupt();
      repoLoadExecution = null;
      return;
    }
    const session = {};
    const seededRepoKey = seedKey(seedRepo);
    activeSession = session;
    branch = "";
    error = null;
    suggestedBranch = null;
    pendingLaunchTargetKey = null;
    submitting = false;
    repos = [];
    selectedKey = "";
    reposError = null;
    reposLoading = true;
    const execution = runtime.runCommand(
      executeGeneratedApiRequest("load repositories", (client, signal) => client.GET("/repos", { signal })).pipe(
        Effect.flatMap((loaded) =>
          Effect.sync(() => {
            if (activeSession !== session) return;
            reposLoading = false;
            repos = (loaded ?? []).map(repoOption);
            // Prefer the repo the caller pointed at, then the last one work was
            // started in, then whatever is first in the list.
            const candidates = [seededRepoKey, getLastUsedNewWorkspaceRepoKey()];
            selectedKey =
              candidates.find((key) => key && repos.some((repo) => repo.key === key)) ??
              repos[0]?.key ??
              "";
          }),
        ),
      ),
      {
        operation: "load repositories for a new workspace",
        safeContext: {},
        onFailure: (failure) => {
          if (activeSession !== session) return;
          reposLoading = false;
          reposError = failure instanceof ApiProblemError
            ? apiErrorMessage(failure.problem, "Could not load repositories")
            : "Could not load repositories";
        },
      },
    );
    repoLoadExecution = execution;
    return () => {
      execution.interrupt();
      if (repoLoadExecution === execution) repoLoadExecution = null;
      if (activeSession === session) activeSession = null;
    };
  });

  const repoRows = $derived<TypeaheadOption[]>(
    repos.map((repo) => ({ name: repo.key, label: repo.label, meta: repo.platformHost })),
  );

  const selected = $derived(repos.find((repo) => repo.key === selectedKey) ?? null);

  const repoFallbackLabel = $derived(
    reposLoading ? "Loading repositories…" : "No tracked repositories yet",
  );

  // Branch conflicts are recognized by the stable problem code and read from
  // typed `details`, never from prose or the per-field huma error array.
  function suggestedBranchFrom(requestError: ProblemBody | undefined): string | null {
    if (requestError?.code !== "branchConflict") return null;
    const value = requestError.details?.["suggestedBranch"];
    return typeof value === "string" && value ? value : null;
  }

  function submit(selectedTargetKey?: string): void {
    if (selectedTargetKey !== undefined) {
      pendingLaunchTargetKey = selectedTargetKey;
    }
    const launchTargetKey = pendingLaunchTargetKey;
    if (submitting) return;
    const repo = selected;
    if (!repo) {
      error = "Pick a repository.";
      return;
    }
    const requested = branch.trim();
    // Escape and backdrop clicks dismiss the dialog even mid-request, and a
    // reopen starts a new form; either way this create must stop influencing
    // the UI once its own dialog session is gone.
    const session = activeSession;
    if (session === null || !open) return;
    error = null;
    suggestedBranch = null;
    submitting = true;
    const ref = {
      provider: repo.provider,
      platformHost: repo.platformHost,
      owner: repo.owner,
      name: repo.name,
      repoPath: `${repo.owner}/${repo.name}`,
    };
    const program = executeGeneratedApiRequest("create workspace", (client, signal) =>
      client.POST(providerRepoPath(ref, "/workspaces"), {
        params: { path: providerRouteParams(ref) },
        body: requested ? { branch: requested } : {},
        signal,
      }),
    ).pipe(
      Effect.flatMap((created) =>
        created?.id
          ? Effect.succeed(created.id)
          : Effect.fail(
              InvalidExternalPayload.make({
                operation: "decode create workspace response",
                cause: created,
              }),
            ),
      ),
      Effect.tap((workspaceId) =>
        Effect.sync(() => {
          if (launchTargetKey) queueWorkspaceLaunch(workspaceId, launchTargetKey, undefined);
          // The workspace exists either way, so it stays the last-used repo; only
          // the navigation is abandoned when the user moved on.
          rememberNewWorkspaceRepoKey(repo.key);
        }),
      ),
      Effect.tap((workspaceId) =>
        Effect.sync(() => {
          if (!open || activeSession !== session) return;
          pendingLaunchTargetKey = null;
          onClose();
          if (onCreated) onCreated(workspaceId);
          else navigate(`/terminal/${workspaceId}`);
        }),
      ),
      Effect.ensuring(
        Effect.sync(() => {
          // A stale create must not re-enable the form under a newer one that is
          // still in flight; each open resets this flag for its own session.
          if (open && activeSession === session) submitting = false;
        }),
      ),
    );
    runtime.runCommand(program, {
      operation: "create workspace",
      safeContext: {
        provider: repo.provider,
        platformHost: repo.platformHost,
        owner: repo.owner,
        name: repo.name,
      },
      onFailure: (failure) => {
        if (!open || activeSession !== session) return;
        if (failure instanceof ApiProblemError) {
          suggestedBranch = suggestedBranchFrom(failure.problem);
          error = apiErrorMessage(failure.problem, "Could not create workspace");
          return;
        }
        error = "Could not create workspace";
      },
    });
  }

  function useSuggestedBranch(): void {
    if (!suggestedBranch) return;
    branch = suggestedBranch;
    suggestedBranch = null;
    error = null;
  }
</script>

<Modal {open} title="New workspace" width={440} frameId="new-workspace" {onClose}>
  <form
    class="new-workspace-form"
    onsubmit={(event) => {
      event.preventDefault();
      submit();
    }}
  >
    <div class="field repo-field">
      <span class="field-label">Repository</span>
      <Typeahead
        options={repoRows}
        value={selectedKey}
        fallbackLabel={repoFallbackLabel}
        placeholder="Filter repositories"
        title="Repository"
        emptyLabel="No repositories match"
        loading={reposLoading}
        error={reposError ?? ""}
        disabled={submitting}
        onselect={(value) => {
          selectedKey = value;
          reposError = null;
        }}
      />
    </div>

    <label class="field">
      <span class="field-label">Branch</span>
      <TextInput
        bind:value={branch}
        block
        placeholder="(generated when empty)"
        ariaLabel="Branch name"
        disabled={submitting}
      />
      <small class="field-hint">
        <GitBranchIcon size="11" strokeWidth="2" aria-hidden="true" />
        Branches from the repository's default branch in a new worktree.
      </small>
    </label>

    {#if error}
      <p class="form-error" role="alert">
        {error}
        {#if suggestedBranch}
          <Button size="sm" onclick={useSuggestedBranch}>
            Use {suggestedBranch}
          </Button>
        {/if}
      </p>
    {/if}

    <div class="form-actions">
      <Button onclick={onClose} disabled={submitting}>Cancel</Button>
      <WorkspaceCreateSplitButton
        label="Create workspace"
        busyLabel="Creating…"
        launchTargets={settings.getLaunchTargets()}
        busy={submitting}
        disabled={selected === null}
        disabledReason={selected === null ? "Pick a repository." : ""}
        surface="solid"
        primaryType="submit"
        onCreate={submit}
      />
    </div>
  </form>
</Modal>

<style>
  .new-workspace-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  /* The picker defaults to a 300px cap, which reads as a misaligned control
     next to the full-width branch input. */
  .repo-field {
    --typeahead-min-width: 0;
    --typeahead-max-width: 100%;
  }

  .field-label {
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .field-hint {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .form-error {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    padding: 6px 8px;
    background: var(--bg-error-subtle, #ffebe9);
    color: var(--text-error, #cf222e);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-xs);
  }

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }
</style>
