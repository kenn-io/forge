<script lang="ts">
  import { tick, untrack } from "svelte";
  import type { ConfigRepo, Settings } from "@middleman/ui/api/types";
  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import {
    bulkAddRepos,
    previewRepos,
    removeRepo,
    updateRepoWorktreeBasePath,
    type RepoPreviewRow,
  } from "../../api/settings.js";

  interface Props {
    open: boolean;
    repo: ConfigRepo | null;
    onClose: () => void;
    onPromoted: (settings: Settings) => void;
  }

  let { open, repo, onClose, onPromoted }: Props = $props();

  let rows = $state.raw<RepoPreviewRow[]>([]);
  let selectedKey = $state<string | null>(null);
  let pathDrafts = $state<Record<string, string>>({});
  let addedExactKeys = $state<Record<string, boolean>>({});
  let filterText = $state("");
  let loading = $state(false);
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let requestToken = 0;
  let loadedRepoKey: string | null = null;
  let searchInput = $state<HTMLInputElement | null>(null);

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
    void tick().then(() => searchInput?.focus());
    untrack(() => { void loadMatches(target); });
  });

  $effect(() => {
    if (!open) return;
    return untrack(() => pushModalFrame("repo-promote-modal", []));
  });

  function promoteRowKey(row: RepoPreviewRow): string {
    return `${row.provider}/${row.platform_host}/${row.repo_path}`.toLowerCase();
  }

  function configRepoKey(target: ConfigRepo): string {
    return `${target.provider}/${target.platform_host}/${target.repo_path || `${target.owner}/${target.name}`}`.toLowerCase();
  }

  function resetAll(): void {
    rows = [];
    selectedKey = null;
    pathDrafts = {};
    addedExactKeys = {};
    filterText = "";
    loading = false;
    submitting = false;
    error = null;
    requestToken += 1;
  }

  async function loadMatches(target: ConfigRepo): Promise<void> {
    const token = ++requestToken;
    rows = [];
    selectedKey = null;
    pathDrafts = {};
    addedExactKeys = {};
    loading = true;
    error = null;
    try {
      const resp = await previewRepos(target.owner, target.name, {
        provider: target.provider,
        host: target.platform_host,
      });
      if (token !== requestToken) return;
      rows = resp.repos;
      const firstAvailable = resp.repos.find((row) => !row.already_configured);
      selectedKey = firstAvailable ? promoteRowKey(firstAvailable) : null;
    } catch (err) {
      if (token !== requestToken) return;
      error = err instanceof Error ? err.message : String(err);
    } finally {
      if (token === requestToken) loading = false;
    }
  }

  async function handlePromote(): Promise<void> {
    const row = selectedRow;
    const key = selectedKey;
    if (!row || !key || row.already_configured) return;
    const worktreeBasePath = selectedPath.trim();
    if (worktreeBasePath === "") return;
    let addedThisAttempt = false;
    submitting = true;
    error = null;
    try {
      if (!addedExactKeys[key]) {
        await bulkAddRepos([
          {
            provider: row.provider,
            host: row.platform_host,
            owner: row.owner,
            name: row.name,
            repo_path: row.repo_path,
          },
        ]);
        addedThisAttempt = true;
        addedExactKeys = { ...addedExactKeys, [key]: true };
      }
      const settings = await updateRepoWorktreeBasePath(
        row.owner,
        row.name,
        {
          provider: row.provider,
          host: row.platform_host,
        },
        worktreeBasePath,
      );
      onPromoted(settings);
      onClose();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      if (addedThisAttempt) {
        try {
          await removeRepo(row.owner, row.name, {
            provider: row.provider,
            host: row.platform_host,
          });
          addedExactKeys = { ...addedExactKeys, [key]: false };
        } catch (rollbackErr) {
          const rollbackMessage = rollbackErr instanceof Error ? rollbackErr.message : String(rollbackErr);
          error = `${message}; rollback failed: ${rollbackMessage}`;
          return;
        }
      }
      error = message;
    } finally {
      submitting = false;
    }
  }

  function closeIfAllowed(): void {
    if (!submitting) onClose();
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") {
      closeIfAllowed();
      return;
    }
    if (event.key !== "Tab") return;
    const container = event.currentTarget;
    if (!(container instanceof HTMLElement)) return;
    const modal = container.querySelector("[role='dialog']");
    if (!(modal instanceof HTMLElement)) return;
    const focusable = Array.from(
      modal.querySelectorAll<HTMLElement>(
        "button:not(:disabled), input:not(:disabled), [tabindex]:not([tabindex='-1'])",
      ),
    );
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }
</script>

{#if open && repo}
  <div class="modal-backdrop" role="presentation" onkeydown={handleKeydown}>
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="repo-promote-title">
      <header class="modal-header">
        <div>
          <h2 id="repo-promote-title">Promote wildcard repository</h2>
          <p>{repo.repo_path || `${repo.owner}/${repo.name}`}</p>
        </div>
        <button type="button" class="close-btn" aria-label="Close" onclick={closeIfAllowed}>×</button>
      </header>

      <label class="search-field">
        <span>Search matches</span>
        <input
          bind:this={searchInput}
          value={filterText}
          placeholder="Filter repositories..."
          disabled={submitting}
          oninput={(event) => { filterText = event.currentTarget.value; }}
        />
      </label>

      {#if error}
        <div class="error-msg" role="alert">{error}</div>
      {/if}

      {#if loading}
        <div class="empty-state">Loading matches...</div>
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
        <div class="empty-state">No matching repositories.</div>
      {/if}

      {#if selectedRow}
        <label class="path-field">
          <span>Local clone path for {selectedRow.repo_path}</span>
          <input
            type="text"
            placeholder="/path/to/existing/clone"
            aria-label={`Local clone path for ${selectedRow.repo_path}`}
            value={selectedKey ? (pathDrafts[selectedKey] ?? "") : ""}
            disabled={submitting || selectedRow.already_configured}
            oninput={(event) => {
              if (!selectedKey) return;
              pathDrafts = {
                ...pathDrafts,
                [selectedKey]: event.currentTarget.value,
              };
            }}
            onkeydown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                void handlePromote();
              }
            }}
          />
        </label>
      {/if}

      <footer class="modal-footer">
        <span>{availableCount} available of {rows.length} matches</span>
        <div class="footer-actions">
          <button class="secondary-btn" type="button" onclick={closeIfAllowed} disabled={submitting}>Cancel</button>
          <button
            class="submit-btn"
            type="button"
            onclick={() => void handlePromote()}
            disabled={submitting || !selectedRow || selectedRow.already_configured || selectedPath.trim() === ""}
          >
            {submitting ? "Promoting..." : "Promote repository"}
          </button>
        </div>
      </footer>
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed;
    inset: 0;
    z-index: 40;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    background: color-mix(in srgb, black 38%, transparent);
  }
  .modal {
    width: min(760px, 100%);
    max-height: min(720px, 92vh);
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 18px;
    color: var(--text-primary);
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    box-shadow: 0 24px 80px rgb(0 0 0 / 35%);
  }
  .modal-header,
  .modal-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
  }
  h2 {
    margin: 0;
    font-size: var(--font-size-lg);
  }
  p {
    margin: 4px 0 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
  .close-btn {
    color: var(--text-muted);
    font-size: var(--font-size-xl);
  }
  .search-field,
  .path-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }
  input[type="text"],
  .search-field input {
    min-width: 0;
    padding: 7px 10px;
    color: var(--text-primary);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-md);
  }
  input:focus {
    border-color: var(--accent-blue);
    outline: none;
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
    gap: 10px;
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
  .empty-state {
    border: 1px dashed var(--border-muted);
    border-radius: var(--radius-md);
    padding: 24px;
    color: var(--text-muted);
    text-align: center;
    font-size: var(--font-size-sm);
  }
  .secondary-btn,
  .submit-btn {
    padding: 7px 14px;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-md);
    font-weight: 600;
  }
  .secondary-btn {
    color: var(--text-secondary);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
  }
  .submit-btn {
    color: white;
    background: var(--accent-blue);
  }
  button:disabled,
  input:disabled {
    cursor: not-allowed;
    opacity: 0.5;
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
