<script lang="ts">
  import { Effect } from "effect";
  import { tick } from "svelte";
  import { Button, IconButton, TextInput } from "@kenn-io/kit-ui";
  import { getStores } from "../../context.js";
  import type { ConfigRepo } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import XIcon from "@lucide/svelte/icons/x";
  import ProviderIcon from "../provider/ProviderIcon.svelte";
  import RepoImportModal from "./RepoImportModal.svelte";
  import RepoPromoteModal from "./RepoPromoteModal.svelte";

  const runtime = getAppRuntime();
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

  function handleAdd(): void {
    if (embedded) return;
    const trimmed = inputValue.trim();
    if (!trimmed) return;
    const parts = trimmed.split("/");
    const [provider, owner, name] = parts;
    if (parts.length !== 3 || !provider || !owner || !name) {
      addError = "Format: provider/owner/name";
      return;
    }
    adding = true;
    addError = null;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.addRepo(owner, name, { provider });
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => showFlash(settingsErrorMessage(failure), { tone: "danger" })),
          onSuccess: (settings) =>
            Effect.sync(() => {
              inputValue = "";
              onUpdate(settings.repos);
              sync.refreshSyncStatus();
            }),
        }),
        Effect.ensuring(Effect.sync(() => {
          adding = false;
        })),
      ),
      { operation: "add configured repository", safeContext: { provider }, onFailure: () => {} },
    );
  }

  function handleRemove(repo: ConfigRepo): void {
    if (embedded) return;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.removeRepo(repo.owner, repo.name, {
          provider: repo.provider,
          host: repo.platform_host,
        });
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => showFlash(settingsErrorMessage(failure), { tone: "danger" })),
          onSuccess: () =>
            Effect.sync(() => {
              confirmingRemove = null;
              const removedKey = repoKey(repo);
              onUpdate(repos.filter((candidate) => repoKey(candidate) !== removedKey));
              sync.refreshSyncStatus();
            }),
        }),
      ),
      {
        operation: "remove configured repository",
        safeContext: { provider: repo.provider, host: repo.platform_host, repoPath: repoLabel(repo) },
        onFailure: () => {},
      },
    );
  }

  function handleRefresh(repo: ConfigRepo): void {
    if (embedded) return;
    const key = repoKey(repo);
    refreshingByKey = { ...refreshingByKey, [key]: true };
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.refreshRepo(repo.owner, repo.name, {
          provider: repo.provider,
          host: repo.platform_host,
        });
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => showFlash(settingsErrorMessage(failure), { tone: "danger" })),
          onSuccess: (settings) =>
            Effect.sync(() => {
              onUpdate(settings.repos);
              sync.refreshSyncStatus();
            }),
        }),
        Effect.ensuring(Effect.sync(() => {
          refreshingByKey = { ...refreshingByKey, [key]: false };
        })),
      ),
      {
        operation: "refresh configured repository",
        safeContext: { provider: repo.provider, host: repo.platform_host, repoPath: repoLabel(repo) },
        onFailure: () => {},
      },
    );
  }

  function handleWorktreeBaseSave(repo: ConfigRepo): void {
    if (embedded || repo.is_glob) return;
    const key = repoKey(repo);
    savingWorktreeBaseByKey = { ...savingWorktreeBaseByKey, [key]: true };
    const worktreeBasePath = worktreeBaseValue(repo, key).trim();
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.updateRepoWorktreeBasePath(
          repo.owner,
          repo.name,
          { provider: repo.provider, host: repo.platform_host },
          worktreeBasePath,
        );
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => showFlash(settingsErrorMessage(failure), { tone: "danger" })),
          onSuccess: (settings) =>
            Effect.sync(() => {
              const nextDrafts = { ...worktreeBaseDrafts };
              delete nextDrafts[key];
              worktreeBaseDrafts = nextDrafts;
              onUpdate(settings.repos);
            }),
        }),
        Effect.ensuring(Effect.sync(() => {
          savingWorktreeBaseByKey = { ...savingWorktreeBaseByKey, [key]: false };
        })),
      ),
      {
        operation: "save repository worktree base",
        safeContext: { provider: repo.provider, host: repo.platform_host, repoPath: repoLabel(repo) },
        onFailure: () => {},
      },
    );
  }

  function handleInputKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter") {
      e.preventDefault();
      handleAdd();
    }
  }

  function closeImportModal(): void {
    importOpen = false;
    runtime.runCommand(
      Effect.promise(() => tick()).pipe(
        Effect.andThen(Effect.sync(() => importTrigger?.focus())),
      ),
      { operation: "restore repository import focus", safeContext: {}, onFailure: () => {} },
    );
  }
</script>

{#if !embedded}
  <div class="repo-import-entry">
    <Button
      tone="info"
      surface="solid"
      onclick={(event) => {
        if (event.currentTarget instanceof HTMLButtonElement) importTrigger = event.currentTarget;
        importOpen = true;
      }}
    >Add repositories…</Button>
    <p>Preview a glob, filter results, and add selected repositories as exact entries.</p>
  </div>
{/if}

<RepoImportModal
  open={importOpen}
  onClose={closeImportModal}
  onImported={(settings) => {
    onUpdate(settings.repos);
    sync.refreshSyncStatus();
  }}
/>

<RepoPromoteModal
  open={Boolean(promoteRepo)}
  repo={promoteRepo}
  onClose={() => { promoteRepo = null; }}
  onPromoted={(settings) => {
    onUpdate(settings.repos);
    sync.refreshSyncStatus();
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
              onclick={() => handleRemove(repo)}
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
                onclick={() => handleRefresh(repo)}
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
                  handleWorktreeBaseSave(repo);
                }
              }}
            />
            <Button
              size="sm"
              tone="info"
              surface="outline"
              ariaLabel={`Save local clone path for ${repoDisplayLabel(repo)}`}
              onclick={() => handleWorktreeBaseSave(repo)}
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
          onclick={handleAdd}
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
