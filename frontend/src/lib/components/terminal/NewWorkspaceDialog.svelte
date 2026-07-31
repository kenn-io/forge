<script lang="ts">
  import { getStores, WorkspaceCreateSplitButton } from "@kenn-forge/ui";
  import { Button, TextInput, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import GitBranchIcon from "@lucide/svelte/icons/git-branch";
  import { canonicalProvider, providerRepoPath, providerRouteParams } from "@kenn-forge/ui/api/provider-routes";
  import type { Repo } from "@kenn-forge/ui/api/types";
  import { queueWorkspaceLaunch } from "@kenn-forge/ui/stores/workspace-create-pending";
  import Modal from "../shared/Modal.svelte";
  import { apiErrorMessage, client } from "../../api/runtime.js";
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
  let repoFetchVersion = 0;

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
    if (!open) return;
    const version = ++repoFetchVersion;
    branch = "";
    error = null;
    suggestedBranch = null;
    pendingLaunchTargetKey = null;
    submitting = false;
    repos = [];
    selectedKey = "";
    reposError = null;
    reposLoading = true;
    void client
      .GET("/repos")
      .then(({ data, error: requestError }) => {
        if (version !== repoFetchVersion) return;
        reposLoading = false;
        if (requestError) {
          reposError = apiErrorMessage(requestError, "Could not load repositories");
          return;
        }
        repos = (data ?? []).map(repoOption);
        // Prefer the repo the caller pointed at, then the last one work was
        // started in, then whatever is first in the list.
        const candidates = [seedKey(seedRepo), getLastUsedNewWorkspaceRepoKey()];
        selectedKey =
          candidates.find((key) => key && repos.some((repo) => repo.key === key)) ??
          repos[0]?.key ??
          "";
      })
      // A transport failure never reaches the error field above, and leaving
      // the picker on "Loading repositories…" forever hides the reason.
      .catch(() => {
        if (version !== repoFetchVersion) return;
        reposLoading = false;
        reposError = "Could not load repositories";
      });
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
  type APIProblem = {
    code?: string | null;
    details?: Record<string, unknown> | null;
  };

  function suggestedBranchFrom(requestError: APIProblem | undefined): string | null {
    if (requestError?.code !== "branchConflict") return null;
    const value = requestError.details?.["suggestedBranch"];
    return typeof value === "string" && value ? value : null;
  }

  async function submit(selectedTargetKey?: string): Promise<void> {
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
    const version = repoFetchVersion;
    error = null;
    suggestedBranch = null;
    submitting = true;
    try {
      const ref = {
        provider: repo.provider,
        platformHost: repo.platformHost,
        owner: repo.owner,
        name: repo.name,
        repoPath: `${repo.owner}/${repo.name}`,
      };
      const { data, error: requestError } = await client.POST(
        providerRepoPath(ref, "/workspaces"),
        {
          params: { path: providerRouteParams(ref) },
          body: requested ? { branch: requested } : {},
        },
      );
      const current = open && version === repoFetchVersion;
      if (requestError) {
        if (!current) return;
        suggestedBranch = suggestedBranchFrom(requestError);
        error = apiErrorMessage(requestError, "Could not create workspace");
        return;
      }
      if (!data?.id) {
        if (!current) return;
        error = "Could not create workspace";
        return;
      }
      const workspaceId = data.id;
      if (launchTargetKey) {
        queueWorkspaceLaunch(workspaceId, launchTargetKey, undefined);
      }
      // The workspace exists either way, so it stays the last-used repo; only
      // the navigation is abandoned when the user moved on.
      rememberNewWorkspaceRepoKey(repo.key);
      if (!current) return;
      pendingLaunchTargetKey = null;
      onClose();
      if (onCreated) onCreated(workspaceId);
      else navigate(`/terminal/${workspaceId}`);
    } catch {
      // A rejected request would otherwise leave the dialog silent.
      if (open && version === repoFetchVersion) {
        error = "Could not create workspace";
      }
    } finally {
      // A stale create must not re-enable the form under a newer one that is
      // still in flight; each open resets this flag for its own session.
      if (open && version === repoFetchVersion) submitting = false;
    }
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
      void submit();
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
