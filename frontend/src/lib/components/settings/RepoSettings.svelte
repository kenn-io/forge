<script lang="ts">
  import { tick } from "svelte";
  import { getStores } from "@middleman/ui";
  import type { ConfigRepo } from "@middleman/ui/api/types";
  import {
    addRepo,
    removeRepo,
    getSettings,
    refreshRepo,
    updateRepoWorktreeBasePath,
  } from "../../api/settings.js";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import ProviderIcon from "../provider/ProviderIcon.svelte";
  import RepoImportModal from "./RepoImportModal.svelte";
  import RepoPromoteModal from "./RepoPromoteModal.svelte";

  const { sync } = getStores();

  interface Props {
    repos: ConfigRepo[];
    onUpdate: (repos: ConfigRepo[]) => void;
  }

  let { repos, onUpdate }: Props = $props();

  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  const embedded = isEmbedded();

  let importOpen = $state(false);
  let importTrigger = $state<HTMLButtonElement | null>(null);
  let inputValue = $state("");
  let adding = $state(false);
  let addError = $state<string | null>(null);
  let confirmingRemove = $state<string | null>(null);
  let removeError = $state<string | null>(null);
  let refreshingByKey = $state<Record<string, boolean>>({});
  let refreshErrors = $state<Record<string, string>>({});
  let worktreeBaseDrafts = $state<Record<string, string>>({});
  let savingWorktreeBaseByKey = $state<Record<string, boolean>>({});
  let worktreeBaseErrors = $state<Record<string, string>>({});
  let cloneEditorOpen = $state<Record<string, boolean>>({});
  let promoteRepo = $state<ConfigRepo | null>(null);

  const showProviderIcons = $derived.by(() => {
    const providers = new Set(
      repos.map((repo) => repo.provider.trim().toLowerCase()),
    );
    return providers.size > 1;
  });

  function repoKey(repo: ConfigRepo): string {
    return `${repo.provider}/${repo.platform_host}/${repo.repo_path || `${repo.owner}/${repo.name}`}`.toLowerCase();
  }

  function repoLabel(repo: ConfigRepo): string {
    return repo.repo_path || `${repo.owner}/${repo.name}`;
  }

  function repoDisplayLabel(repo: ConfigRepo): string {
    const label = repoLabel(repo);
    return repo.is_glob ? `${label} (${repo.matched_repo_count})` : label;
  }

  function worktreeBaseValue(repo: ConfigRepo, key: string): string {
    return worktreeBaseDrafts[key] ?? repo.worktree_base_path ?? "";
  }

  async function handleAdd(): Promise<void> {
    if (embedded) return;
    const trimmed = inputValue.trim();
    if (!trimmed) return;
    const parts = trimmed.split("/");
    if (parts.length !== 3 || !parts[0] || !parts[1] || !parts[2]) {
      addError = "Format: provider/owner/name";
      return;
    }
    adding = true;
    addError = null;
    try {
      const settings = await addRepo(parts[1], parts[2], {
        provider: parts[0],
      });
      inputValue = "";
      onUpdate(settings.repos);
      void sync.refreshSyncStatus();
    } catch (err) {
      addError = err instanceof Error ? err.message : String(err);
    } finally {
      adding = false;
    }
  }

  async function handleRemove(repo: ConfigRepo): Promise<void> {
    if (embedded) return;
    removeError = null;
    try {
      await removeRepo(repo.owner, repo.name, {
        provider: repo.provider,
        host: repo.platform_host,
      });
      confirmingRemove = null;
      const settings = await getSettings();
      onUpdate(settings.repos);
      void sync.refreshSyncStatus();
    } catch (err) {
      removeError = err instanceof Error ? err.message : String(err);
    }
  }

  async function handleRefresh(repo: ConfigRepo): Promise<void> {
    if (embedded) return;
    const key = repoKey(repo);
    refreshingByKey = { ...refreshingByKey, [key]: true };
    if (refreshErrors[key]) {
      const nextErrors = { ...refreshErrors };
      delete nextErrors[key];
      refreshErrors = nextErrors;
    }
    try {
      const settings = await refreshRepo(repo.owner, repo.name, {
        provider: repo.provider,
        host: repo.platform_host,
      });
      onUpdate(settings.repos);
      void sync.refreshSyncStatus();
    } catch (err) {
      refreshErrors = {
        ...refreshErrors,
        [key]: err instanceof Error ? err.message : String(err),
      };
    } finally {
      refreshingByKey = { ...refreshingByKey, [key]: false };
    }
  }

  async function handleWorktreeBaseSave(repo: ConfigRepo): Promise<void> {
    if (embedded || repo.is_glob) return;
    const key = repoKey(repo);
    savingWorktreeBaseByKey = { ...savingWorktreeBaseByKey, [key]: true };
    if (worktreeBaseErrors[key]) {
      const nextErrors = { ...worktreeBaseErrors };
      delete nextErrors[key];
      worktreeBaseErrors = nextErrors;
    }
    try {
      const settings = await updateRepoWorktreeBasePath(
        repo.owner,
        repo.name,
        {
          provider: repo.provider,
          host: repo.platform_host,
        },
        worktreeBaseValue(repo, key).trim(),
      );
      const nextDrafts = { ...worktreeBaseDrafts };
      delete nextDrafts[key];
      worktreeBaseDrafts = nextDrafts;
      onUpdate(settings.repos);
    } catch (err) {
      worktreeBaseErrors = {
        ...worktreeBaseErrors,
        [key]: err instanceof Error ? err.message : String(err),
      };
    } finally {
      savingWorktreeBaseByKey = { ...savingWorktreeBaseByKey, [key]: false };
    }
  }

  function handleInputKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter") {
      e.preventDefault();
      void handleAdd();
    }
  }

  async function closeImportModal(): Promise<void> {
    importOpen = false;
    await tick();
    importTrigger?.focus();
  }
</script>

{#if !embedded}
  <div class="repo-import-entry">
    <button bind:this={importTrigger} class="primary-import-btn" type="button" onclick={() => { importOpen = true; }}>Add repositories…</button>
    <p>Preview a glob, filter results, and add selected repositories as exact entries.</p>
  </div>
{/if}

<RepoImportModal
  open={importOpen}
  onClose={() => { void closeImportModal(); }}
  onImported={(settings) => {
    onUpdate(settings.repos);
    void sync.refreshSyncStatus();
  }}
/>

<RepoPromoteModal
  open={Boolean(promoteRepo)}
  repo={promoteRepo}
  onClose={() => { promoteRepo = null; }}
  onPromoted={(settings) => {
    onUpdate(settings.repos);
    void sync.refreshSyncStatus();
  }}
/>

<div class="repo-list">
  {#each repos as repo (repoKey(repo))}
    {@const key = repoKey(repo)}
    <div class="repo-row">
      <div class="repo-line">
        <div class="repo-main">
          <span class="repo-name">{#if showProviderIcons}<ProviderIcon provider={repo.provider} size={16} class="repo-provider-icon" />{/if}{repoDisplayLabel(repo)}</span>
          {#if refreshErrors[key]}
            <div class="error-msg row-error">{refreshErrors[key]}</div>
          {/if}
        </div>
        {#if confirmingRemove === key}
          <span class="confirm-prompt">
            Remove?
            <button class="confirm-btn confirm-yes" onclick={() => void handleRemove(repo)}>Yes</button>
            <button class="confirm-btn confirm-no" onclick={() => { confirmingRemove = null; removeError = null; }}>No</button>
          </span>
        {:else}
          <div class="repo-actions">
            {#if repo.is_glob}
              <button
                class="promote-btn"
                onclick={() => { promoteRepo = repo; }}
                disabled={embedded}
                aria-label={`Promote glob repository ${repoLabel(repo)}`}
              >
                Promote
              </button>
              <button
                class="refresh-btn"
                onclick={() => void handleRefresh(repo)}
                disabled={Boolean(refreshingByKey[key])}
              >
                {refreshingByKey[key] ? "Refreshing..." : "Refresh"}
              </button>
            {:else}
              <button
                class={["clone-btn", { configured: Boolean(repo.worktree_base_path), open: Boolean(cloneEditorOpen[key]) }]}
                aria-label={`Local clone for ${repoDisplayLabel(repo)}`}
                aria-expanded={Boolean(cloneEditorOpen[key])}
                title={repo.worktree_base_path ? `Local clone: ${repo.worktree_base_path}` : "Set local clone"}
                onclick={() => {
                  cloneEditorOpen = { ...cloneEditorOpen, [key]: !cloneEditorOpen[key] };
                }}
              ><SettingsIcon size={14} aria-hidden="true" /></button>
            {/if}
            <button
              class="remove-btn"
              title={`Remove ${key}`}
              onclick={() => {
                confirmingRemove = key;
                removeError = null;
                if (refreshErrors[key]) {
                  const nextErrors = { ...refreshErrors };
                  delete nextErrors[key];
                  refreshErrors = nextErrors;
                }
              }}
            >&times;</button>
          </div>
        {/if}
      </div>
      {#if !repo.is_glob && cloneEditorOpen[key]}
        <div class="worktree-base-body">
          <div class="worktree-base-control">
            <input
              id={`worktree-base-${key}`}
              class="worktree-base-input"
              type="text"
              placeholder="/path/to/existing/clone"
              aria-label={`Local clone path for ${repoDisplayLabel(repo)}`}
              value={worktreeBaseValue(repo, key)}
              disabled={embedded || Boolean(savingWorktreeBaseByKey[key])}
              oninput={(event) => {
                worktreeBaseDrafts = {
                  ...worktreeBaseDrafts,
                  [key]: event.currentTarget.value,
                };
              }}
              onkeydown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void handleWorktreeBaseSave(repo);
                }
              }}
            />
            <button
              class="worktree-base-save"
              aria-label={`Save local clone path for ${repoDisplayLabel(repo)}`}
              onclick={() => void handleWorktreeBaseSave(repo)}
              disabled={embedded || Boolean(savingWorktreeBaseByKey[key]) || worktreeBaseValue(repo, key).trim() === (repo.worktree_base_path ?? "")}
            >
              {savingWorktreeBaseByKey[key] ? "Saving..." : "Save"}
            </button>
          </div>
          <p class="worktree-base-hint">
            Workspaces are created as worktrees of this clone instead of starting from a fresh clone.
          </p>
          {#if worktreeBaseErrors[key]}
            <div class="error-msg row-error">{worktreeBaseErrors[key]}</div>
          {/if}
        </div>
      {/if}
    </div>
  {/each}
</div>

{#if removeError}
  <div class="error-msg">{removeError}</div>
{/if}

{#if !embedded}
  <details class="advanced-add">
    <summary>Advanced: add provider-scoped repo or tracking glob directly</summary>
    <div class="advanced-body">
      <div class="add-form">
        <input class="add-input" type="text" placeholder="provider/owner/name" bind:value={inputValue} onkeydown={handleInputKeydown} disabled={adding} />
        <button class="add-btn" onclick={() => void handleAdd()} disabled={adding || !inputValue.trim()}>
          {adding ? "Adding..." : "Add"}
        </button>
      </div>

      {#if addError}
        <div class="error-msg">{addError}</div>
      {/if}
    </div>
  </details>
{/if}

<style>
  .repo-import-entry { display: flex; flex-direction: column; gap: 4px; padding-bottom: 12px; border-bottom: 1px solid var(--border-muted); }
  .primary-import-btn { align-self: flex-start; padding: 6px 14px; font-size: var(--font-size-md); font-weight: 600; color: white; background: var(--accent-blue); border-radius: var(--radius-sm); }
  .repo-import-entry p { margin: 0; color: var(--text-muted); font-size: var(--font-size-sm); }
  .advanced-add { padding-top: 8px; }
  .advanced-add summary { cursor: pointer; color: var(--text-secondary); font-size: var(--font-size-sm); }
  .advanced-body { padding-top: 8px; display: flex; flex-direction: column; gap: 6px; }
  .repo-list { display: flex; flex-direction: column; }
  .repo-row {
    display: flex; flex-direction: column; gap: 6px;
    padding: 8px 0; border-bottom: 1px solid var(--border-muted);
  }
  .repo-row:last-child { border-bottom: none; }
  .repo-line { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .repo-main { display: flex; flex-direction: column; gap: 4px; min-width: 0; flex: 1; }
  .repo-name { display: inline-flex; align-items: center; gap: 6px; font-size: var(--font-size-md); color: var(--text-primary); font-weight: 500; }
  :global(.repo-provider-icon) { color: var(--text-secondary); }
  .clone-btn {
    display: inline-flex; align-items: center; color: var(--text-muted);
    padding: 3px 6px; border-radius: var(--radius-sm);
    transition: color 0.1s, background 0.1s;
  }
  .clone-btn:hover, .clone-btn.open {
    color: var(--accent-blue); background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
  }
  .clone-btn.configured { color: var(--accent-blue); }
  .worktree-base-body { display: flex; flex-direction: column; gap: 4px; }
  .worktree-base-control { display: flex; gap: 8px; }
  .worktree-base-input {
    flex: 1; min-width: 0; font-size: var(--font-size-sm); padding: 5px 8px;
    color: var(--text-primary); background: var(--bg-inset);
    border: 1px solid var(--border-muted); border-radius: var(--radius-sm);
    font-family: var(--font-mono);
  }
  .worktree-base-input:focus { border-color: var(--accent-blue); outline: none; }
  .worktree-base-save {
    flex-shrink: 0; padding: 4px 10px; font-size: var(--font-size-sm); font-weight: 600;
    color: var(--accent-blue); border: 1px solid color-mix(in srgb, var(--accent-blue) 35%, var(--border-muted));
    border-radius: var(--radius-sm);
  }
  .worktree-base-save:hover:not(:disabled) { background: color-mix(in srgb, var(--accent-blue) 10%, transparent); }
  .worktree-base-save:disabled { opacity: 0.45; cursor: not-allowed; }
  .worktree-base-hint { margin: 0; color: var(--text-muted); font-size: var(--font-size-xs); }
  .repo-actions { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
  .refresh-btn {
    padding: 4px 10px; font-size: var(--font-size-sm); font-weight: 500;
    color: var(--accent-blue); border: 1px solid color-mix(in srgb, var(--accent-blue) 35%, var(--border-muted));
    border-radius: var(--radius-sm); transition: background 0.12s, opacity 0.12s;
  }
  .promote-btn {
    padding: 4px 10px; font-size: var(--font-size-sm); font-weight: 600;
    color: var(--text-primary); background: var(--bg-inset);
    border: 1px solid var(--border-muted); border-radius: var(--radius-sm);
    transition: background 0.12s, border-color 0.12s, opacity 0.12s;
  }
  .promote-btn:hover:not(:disabled) {
    border-color: color-mix(in srgb, var(--accent-blue) 35%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-blue) 8%, var(--bg-inset));
  }
  .promote-btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .refresh-btn:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
  }
  .refresh-btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .remove-btn {
    font-size: var(--font-size-lg); color: var(--text-muted); padding: 2px 6px;
    border-radius: var(--radius-sm); line-height: 1; transition: color 0.1s, background 0.1s;
  }
  .remove-btn:hover:not(:disabled) {
    color: var(--accent-red); background: color-mix(in srgb, var(--accent-red) 10%, transparent);
  }
  .remove-btn:disabled { opacity: 0.3; cursor: not-allowed; }
  .confirm-prompt { font-size: var(--font-size-sm); color: var(--text-secondary); display: flex; align-items: center; gap: 6px; }
  .confirm-btn { font-size: var(--font-size-xs); font-weight: 600; padding: 2px 8px; border-radius: var(--radius-sm); }
  .confirm-yes { color: var(--accent-red); border: 1px solid var(--accent-red); }
  .confirm-yes:hover { background: color-mix(in srgb, var(--accent-red) 10%, transparent); }
  .confirm-no { color: var(--text-muted); border: 1px solid var(--border-muted); }
  .confirm-no:hover { background: var(--bg-surface-hover); }
  .add-form { display: flex; gap: 8px; }
  .add-input {
    flex: 1; font-size: var(--font-size-md); padding: 6px 10px;
    background: var(--bg-inset); border: 1px solid var(--border-muted); border-radius: var(--radius-sm);
  }
  .add-input:focus { border-color: var(--accent-blue); outline: none; }
  .add-btn {
    padding: 6px 14px; font-size: var(--font-size-md); font-weight: 500; color: white;
    background: var(--accent-blue); border-radius: var(--radius-sm); transition: opacity 0.12s;
  }
  .add-btn:hover:not(:disabled) { opacity: 0.9; }
  .add-btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .error-msg { font-size: var(--font-size-sm); color: var(--accent-red); padding: 4px 0; }
  .row-error { padding: 0; }
</style>
