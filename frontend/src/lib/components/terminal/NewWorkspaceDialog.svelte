<script lang="ts">
  import { Button, TextInput, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import GitBranchIcon from "@lucide/svelte/icons/git-branch";
  import { canonicalProvider, providerRepoPath, providerRouteParams } from "@middleman/ui/api/provider-routes";
  import type { Repo } from "@middleman/ui/api/types";
  import Modal from "../shared/Modal.svelte";
  import { apiErrorMessage, client } from "../../api/runtime.js";
  import { navigate } from "../../stores/router.svelte.js";
  import type { NewWorkspaceRepoSeed } from "../../stores/new-workspace.svelte.js";

  // Starts new work in a tracked repository without a pull request, issue, or
  // Kata task: pick the repo, optionally name the branch, and middleman
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
  // from a previous open must not repopulate the list.
  $effect(() => {
    if (!open) return;
    const version = ++repoFetchVersion;
    branch = "";
    error = null;
    suggestedBranch = null;
    submitting = false;
    reposError = null;
    reposLoading = true;
    void client.GET("/repos").then(({ data, error: requestError }) => {
      if (version !== repoFetchVersion) return;
      reposLoading = false;
      if (requestError) {
        reposError = apiErrorMessage(requestError, "Could not load repositories");
        return;
      }
      repos = (data ?? []).map(repoOption);
      const preferred = seedKey(seedRepo);
      selectedKey = repos.some((repo) => repo.key === preferred)
        ? preferred
        : (repos[0]?.key ?? "");
    });
  });

  const repoRows = $derived<TypeaheadOption[]>(
    repos.map((repo) => ({ name: repo.key, label: repo.label, meta: repo.platformHost })),
  );

  const selected = $derived(repos.find((repo) => repo.key === selectedKey) ?? null);

  const repoFallbackLabel = $derived(
    reposLoading ? "Loading repositories…" : "No tracked repositories yet",
  );

  type APIErrorDetails = {
    errors?: { location?: string; value?: unknown }[] | null;
  };

  function conflictValue(
    requestError: APIErrorDetails | undefined,
    location: string,
  ): string | null {
    const value = requestError?.errors?.find((entry) => entry.location === location)?.value;
    return typeof value === "string" && value ? value : null;
  }

  async function submit(): Promise<void> {
    if (submitting) return;
    const repo = selected;
    if (!repo) {
      error = "Pick a repository.";
      return;
    }
    const requested = branch.trim();
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
      if (requestError) {
        suggestedBranch = conflictValue(requestError, "body.suggested_branch");
        error = apiErrorMessage(requestError, "Could not create workspace");
        return;
      }
      if (!data?.id) {
        error = "Could not create workspace";
        return;
      }
      const workspaceId = data.id;
      onClose();
      if (onCreated) onCreated(workspaceId);
      else navigate(`/terminal/${workspaceId}`);
    } finally {
      submitting = false;
    }
  }

  function useSuggestedBranch(): void {
    if (!suggestedBranch) return;
    branch = suggestedBranch;
    suggestedBranch = null;
    error = null;
  }
</script>

<Modal {open} title="New workspace" width={520} frameId="new-workspace" {onClose}>
  <form
    class="new-workspace-form"
    onsubmit={(event) => {
      event.preventDefault();
      void submit();
    }}
  >
    <div class="field">
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
      <Button
        type="submit"
        tone="info"
        surface="solid"
        disabled={submitting || selected === null}
      >
        {submitting ? "Creating…" : "Create workspace"}
      </Button>
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
