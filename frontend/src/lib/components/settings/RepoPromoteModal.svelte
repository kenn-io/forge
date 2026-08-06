<script lang="ts">
  import { Button, EmptyState, SearchInput, Spinner, TextInput } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { onDestroy, tick, untrack } from "svelte";
  import type { ConfigRepo, Settings } from "../../api/types.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import {
    SettingsWorkflow,
    settingsErrorMessage,
    type RepoPreviewRow,
  } from "../../stores/settings-workflow.js";
  import Modal from "../shared/Modal.svelte";

  interface Props {
    open: boolean;
    repo: ConfigRepo | null;
    onClose: () => void;
    onPromoted: (settings: Settings) => void;
  }

  let { open, repo, onClose, onPromoted }: Props = $props();
  const runtime = getAppRuntime();

  let rows = $state.raw<RepoPreviewRow[]>([]);
  let selectedKey = $state<string | null>(null);
  let pathDrafts = $state<Record<string, string>>({});
  let addedExactKeys = $state<Record<string, boolean>>({});
  let filterText = $state("");
  let loading = $state(false);
  let submitting = $state(false);
  let stateUncertain = $state(false);
  let error = $state<string | null>(null);
  let previewExecution: AppExecution<void, never> | null = null;
  let loadedRepoKey: string | null = null;
  // kit SearchInput's inputEl bindable is exactly-optional, which
  // exactOptionalPropertyTypes rejects for a `| undefined` binding —
  // resolve the inner input through the wrapper instead.
  let searchWrap = $state<HTMLDivElement>();

  const filteredRows = $derived.by(() => {
    const query = filterText.trim().toLowerCase();
    if (query === "") return rows;
    return rows.filter((row) => {
      const haystack = [
        row.repo_path,
        row.owner,
        row.name,
        row.description ?? "",
      ].join(" ").toLowerCase();
      return haystack.includes(query);
    });
  });

  const selectedRow = $derived.by(() => {
    if (!selectedKey) return null;
    return rows.find((row) => promoteRowKey(row) === selectedKey) ?? null;
  });

  const selectedPath = $derived(
    selectedKey ? (pathDrafts[selectedKey] ?? "") : "",
  );
  const availableCount = $derived(rows.filter((row) => !row.already_configured).length);

  $effect(() => {
    const target = repo;
    if (!open || !target) {
      loadedRepoKey = null;
      untrack(resetAll);
      return;
    }
    const key = configRepoKey(target);
    if (loadedRepoKey === key) return;
    loadedRepoKey = key;
    runtime.runCommand(
      Effect.promise(() => tick()).pipe(
        Effect.tap(() => Effect.sync(() => {
          if (open && repo && configRepoKey(repo) === key) searchWrap?.querySelector("input")?.focus();
        })),
      ),
      { operation: "focus repository promotion search", safeContext: {}, onFailure: () => {} },
    );
    untrack(() => launchMatchesLoad(target));
  });

  onDestroy(() => previewExecution?.interrupt());

  function promoteRowKey(row: RepoPreviewRow): string {
    return `${row.provider}/${row.platform_host}/${row.repo_path}`.toLowerCase();
  }

  function configRepoKey(target: ConfigRepo): string {
    return `${target.provider}/${target.platform_host}/${target.repo_path || `${target.owner}/${target.name}`}`.toLowerCase();
  }

  function resetAll(): void {
    previewExecution?.interrupt();
    previewExecution = null;
    rows = [];
    selectedKey = null;
    pathDrafts = {};
    addedExactKeys = {};
    filterText = "";
    loading = false;
    submitting = false;
    stateUncertain = false;
    error = null;
  }

  function launchMatchesLoad(target: ConfigRepo): void {
    previewExecution?.interrupt();
    rows = [];
    selectedKey = null;
    pathDrafts = {};
    addedExactKeys = {};
    loading = true;
    error = null;
    const targetKey = configRepoKey(target);
    let execution: AppExecution<void, never> | undefined;
    const isCurrent = () =>
      execution !== undefined && previewExecution === execution && open && repo !== null && configRepoKey(repo) === targetKey;
    const program = Effect.gen(function* () {
      const workflow = yield* SettingsWorkflow;
      return yield* workflow.previewRepos(target.owner, target.name, {
        provider: target.provider,
        host: target.platform_host,
      });
    }).pipe(
      Effect.matchEffect({
        onFailure: (failure) => Effect.sync(() => {
          if (!isCurrent()) return;
          error = settingsErrorMessage(failure);
        }),
        onSuccess: (response) => Effect.sync(() => {
          if (!isCurrent()) return;
          rows = response.repos;
          const firstAvailable = response.repos.find((row) => !row.already_configured);
          selectedKey = firstAvailable ? promoteRowKey(firstAvailable) : null;
        }),
      }),
      Effect.ensuring(Effect.sync(() => {
        if (!isCurrent()) return;
        previewExecution = null;
        loading = false;
      })),
    );
    execution = runtime.runCommand(program, {
      operation: "load wildcard repository matches",
      safeContext: { provider: target.provider, host: target.platform_host },
      onFailure: () => {},
    });
    previewExecution = execution;
  }

  function handlePromote(): void {
    const row = selectedRow;
    const key = selectedKey;
    if (!row || !key || row.already_configured || stateUncertain) return;
    const worktreeBasePath = selectedPath.trim();
    if (worktreeBasePath === "") return;
    const exactRepoAlreadyAdded = addedExactKeys[key] ?? false;
    submitting = true;
    error = null;
    const repoInput = {
      provider: row.provider,
      host: row.platform_host,
      owner: row.owner,
      name: row.name,
      repo_path: row.repo_path,
    };
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.promoteRepo(repoInput, worktreeBasePath, exactRepoAlreadyAdded);
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) => Effect.sync(() => {
            if (failure._tag === "RepoPromotionRollbackError") {
              addedExactKeys = { ...addedExactKeys, [key]: true };
              onPromoted(failure.settings);
              error = settingsErrorMessage(failure);
              return;
            }
            if (failure._tag === "RepoPromotionStateUncertainError") {
              stateUncertain = true;
              error = settingsErrorMessage(failure);
              return;
            }
            showFlash(settingsErrorMessage(failure), { tone: "danger" });
          }),
          onSuccess: (settings) => Effect.sync(() => {
            addedExactKeys = { ...addedExactKeys, [key]: false };
            onPromoted(settings);
            onClose();
          }),
        }),
        Effect.ensuring(Effect.sync(() => {
          submitting = false;
        })),
      ),
      {
        operation: "promote wildcard repository",
        safeContext: {
          provider: row.provider,
          host: row.platform_host,
          repoPath: row.repo_path,
        },
        onFailure: () => {},
      },
    );
  }

  function closeIfAllowed(): void {
    if (!submitting) onClose();
  }

</script>

<Modal
  open={open && repo !== null}
  title="Promote wildcard repository"
  width={760}
  frameId="repo-promote-modal"
  showClose
  onClose={closeIfAllowed}
>
  <div class="promote-content">
    <p class="promote-subject">{repo?.repo_path || `${repo?.owner}/${repo?.name}`}</p>

      <div class="match-search" bind:this={searchWrap}>
        <span>Search matches</span>
        <SearchInput
          bind:value={filterText}
          block
          placeholder="Filter repositories..."
          disabled={submitting}
          ariaLabel="Search matches"
        />
      </div>

      {#if error}
        <div class="error-msg" role="alert">{error}</div>
      {/if}

      {#if loading}
        <div class="loading-placeholder">
          <Spinner size={14} label="Loading matches" />
          Loading matches...
        </div>
      {:else if filteredRows.length > 0}
        <div class="match-list" role="radiogroup" aria-label="Wildcard matches">
          {#each filteredRows as row (promoteRowKey(row))}
            {@const key = promoteRowKey(row)}
            <label class={["match-row", selectedKey === key && "match-row--selected", row.already_configured && "match-row--disabled"]}>
              <input
                type="radio"
                name="promote-repo"
                checked={selectedKey === key}
                disabled={row.already_configured || submitting}
                onchange={() => { selectedKey = key; }}
              />
              <span class="match-main">
                <span class="match-name">{row.repo_path}</span>
                {#if row.description}
                  <span class="match-description">{row.description}</span>
                {/if}
              </span>
              {#if row.already_configured}
                <span class="match-status">Configured</span>
              {/if}
            </label>
          {/each}
        </div>
      {:else}
        <EmptyState title="No matching repositories." />
      {/if}

      {#if selectedRow}
        <label class="path-field">
          <span>Local clone path for {selectedRow.repo_path}</span>
          <TextInput
            block
            placeholder="/path/to/existing/clone"
            ariaLabel={`Local clone path for ${selectedRow.repo_path}`}
            value={selectedKey ? (pathDrafts[selectedKey] ?? "") : ""}
            disabled={submitting || selectedRow.already_configured}
            oninput={(value) => {
              if (!selectedKey) return;
              pathDrafts = {
                ...pathDrafts,
                [selectedKey]: value,
              };
            }}
            onkeydown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                handlePromote();
              }
            }}
          />
        </label>
      {/if}

  </div>
  {#snippet footer()}
    <span class="footer-status">{availableCount} available of {rows.length} matches</span>
    <div class="footer-actions">
      <Button onclick={closeIfAllowed} disabled={submitting}>Cancel</Button>
      <Button
        tone="info"
        surface="solid"
        onclick={handlePromote}
        disabled={stateUncertain || submitting || !selectedRow || selectedRow.already_configured || selectedPath.trim() === ""}
      >
        {submitting ? "Promoting..." : "Promote repository"}
      </Button>
    </div>
  {/snippet}
</Modal>

<style>
  .loading-placeholder {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    padding: var(--space-8) var(--space-6);
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .promote-content {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }
  .promote-subject {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
  .footer-status {
    margin-right: auto;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
  .match-search,
  .path-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }
  .match-list {
    min-height: 0;
    overflow: auto;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
  }
  .match-row {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    min-height: 48px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-muted);
    cursor: pointer;
  }
  .match-row:last-child {
    border-bottom: 0;
  }
  .match-row:hover {
    background: var(--bg-surface-hover);
  }
  .match-row--selected {
    background: color-mix(in srgb, var(--accent-blue) 8%, transparent);
  }
  .match-row--disabled {
    cursor: not-allowed;
    opacity: 0.62;
  }
  .match-main {
    display: flex;
    min-width: 0;
    flex: 1;
    flex-direction: column;
    gap: 2px;
  }
  .match-name {
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 600;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .match-description {
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .match-status {
    flex-shrink: 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  .error-msg {
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }
  .footer-actions {
    display: flex;
    gap: 8px;
  }
</style>
