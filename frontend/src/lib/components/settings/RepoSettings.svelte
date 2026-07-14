<script lang="ts">
  import { tick } from "svelte";
  import { Button, IconButton, TextInput } from "@kenn-io/kit-ui";
  import { getStores } from "@middleman/ui";
  import type { ConfigRepo } from "@middleman/ui/api/types";
  import { showFlash } from "@middleman/ui/stores/flash";
  import {
    addRepo,
    removeRepo,
    getSettings,
    refreshRepo,
    updateRepoWorktreeBasePath,
  } from "../../api/settings.js";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import XIcon from "@lucide/svelte/icons/x";
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
  let refreshingByKey = $state<Record<string, boolean>>({});
  let worktreeBaseDrafts = $state<Record<string, string>>({});
  let savingWorktreeBaseByKey = $state<Record<string, boolean>>({});
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
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    } finally {
      adding = false;
    }
  }

  async function handleRemove(repo: ConfigRepo): Promise<void> {
    if (embedded) return;
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
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    }
  }

  async function handleRefresh(repo: ConfigRepo): Promise<void> {
    if (embedded) return;
    const key = repoKey(repo);
    refreshingByKey = { ...refreshingByKey, [key]: true };
    try {
      const settings = await refreshRepo(repo.owner, repo.name, {
        provider: repo.provider,
        host: repo.platform_host,
      });
      onUpdate(settings.repos);
      void sync.refreshSyncStatus();
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    } finally {
      refreshingByKey = { ...refreshingByKey, [key]: false };
    }
  }

  async function handleWorktreeBaseSave(repo: ConfigRepo): Promise<void> {
    if (embedded || repo.is_glob) return;
    const key = repoKey(repo);
    savingWorktreeBaseByKey = { ...savingWorktreeBaseByKey, [key]: true };
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
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
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
    <Button
      tone="info"
      surface="solid"
      onclick={(event) => {
        importTrigger = event.currentTarget as HTMLButtonElement;
        importOpen = true;
      }}
    >Add repositories…</Button>
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
        </div>
        {#if confirmingRemove === key}
          <span class="confirm-prompt">
            Remove?
            <Button
              size="sm"
              tone="danger"
              surface="outline"
              onclick={() => void handleRemove(repo)}
            >Yes</Button>
            <Button
              size="sm"
              onclick={() => {
                confirmingRemove = null;
              }}
            >No</Button>
          </span>
        {:else}
          <div class="repo-actions">
            {#if repo.is_glob}
              <Button
                size="sm"
                onclick={() => { promoteRepo = repo; }}
                disabled={embedded}
                ariaLabel={`Promote glob repository ${repoLabel(repo)}`}
              >
                Promote
              </Button>
              <Button
                size="sm"
                tone="info"
                surface="soft"
                onclick={() => void handleRefresh(repo)}
                disabled={Boolean(refreshingByKey[key])}
              >
                {refreshingByKey[key] ? "Refreshing..." : "Refresh"}
              </Button>
            {:else}
              <IconButton
                size="sm"
                tone="info"
                ariaLabel={`Local clone for ${repoDisplayLabel(repo)}`}
                ariaExpanded={Boolean(cloneEditorOpen[key])}
                ariaPressed={Boolean(repo.worktree_base_path) || Boolean(cloneEditorOpen[key])}
                title={repo.worktree_base_path ? `Local clone: ${repo.worktree_base_path}` : "Set local clone"}
                onclick={() => {
                  cloneEditorOpen = { ...cloneEditorOpen, [key]: !cloneEditorOpen[key] };
                }}
              ><SettingsIcon size={14} aria-hidden="true" /></IconButton>
            {/if}
            <IconButton
              size="sm"
              tone="danger"
              ariaLabel={`Remove ${repoDisplayLabel(repo)}`}
              title={`Remove ${key}`}
              onclick={() => {
                confirmingRemove = key;
              }}
            ><XIcon size={14} aria-hidden="true" /></IconButton>
          </div>
        {/if}
      </div>
      {#if !repo.is_glob && cloneEditorOpen[key]}
        <div class="worktree-base-body">
          <div class="worktree-base-control">
            <TextInput
              id={`worktree-base-${key}`}
              class="worktree-base-input"
              block
              placeholder="/path/to/existing/clone"
              ariaLabel={`Local clone path for ${repoDisplayLabel(repo)}`}
              value={worktreeBaseValue(repo, key)}
              disabled={embedded || Boolean(savingWorktreeBaseByKey[key])}
              oninput={(value) => {
                worktreeBaseDrafts = {
                  ...worktreeBaseDrafts,
                  [key]: value,
                };
              }}
              onkeydown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void handleWorktreeBaseSave(repo);
                }
              }}
            />
            <Button
              size="sm"
              tone="info"
              surface="outline"
              ariaLabel={`Save local clone path for ${repoDisplayLabel(repo)}`}
              onclick={() => void handleWorktreeBaseSave(repo)}
              disabled={embedded || Boolean(savingWorktreeBaseByKey[key]) || worktreeBaseValue(repo, key).trim() === (repo.worktree_base_path ?? "")}
            >
              {savingWorktreeBaseByKey[key] ? "Saving..." : "Save"}
            </Button>
          </div>
          <p class="worktree-base-hint">
            Workspaces are created as worktrees of this clone instead of starting from a fresh clone.
          </p>
        </div>
      {/if}
    </div>
  {/each}
</div>

{#if !embedded}
  <details class="advanced-add">
    <summary>Advanced: add provider-scoped repo or tracking glob directly</summary>
    <div class="advanced-body">
      <div class="add-form">
        <TextInput
          class="add-input"
          block
          placeholder="provider/owner/name"
          bind:value={inputValue}
          onkeydown={handleInputKeydown}
          disabled={adding}
        />
        <Button
          tone="info"
          surface="solid"
          onclick={() => void handleAdd()}
          disabled={adding || !inputValue.trim()}
        >
          {adding ? "Adding..." : "Add"}
        </Button>
      </div>

      {#if addError}
        <div class="error-msg">{addError}</div>
      {/if}
    </div>
  </details>
{/if}

<style>
  .repo-import-entry { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; padding-bottom: 12px; border-bottom: 1px solid var(--border-muted); }
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
  .worktree-base-body { display: flex; flex-direction: column; gap: 4px; }
  .worktree-base-control { display: flex; gap: 8px; }
  :global(.worktree-base-input) { flex: 1; min-width: 0; font-family: var(--font-mono); }
  .worktree-base-hint { margin: 0; color: var(--text-muted); font-size: var(--font-size-xs); }
  .repo-actions { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
  .confirm-prompt { font-size: var(--font-size-sm); color: var(--text-secondary); display: flex; align-items: center; gap: 6px; }
  .add-form { display: flex; gap: 8px; }
  :global(.add-input) { flex: 1; min-width: 0; }
  .error-msg { font-size: var(--font-size-sm); color: var(--accent-red); padding: 4px 0; }
</style>
