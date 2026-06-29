<script lang="ts">
  import ArrowUp from "@lucide/svelte/icons/arrow-up";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import FolderOpen from "@lucide/svelte/icons/folder-open";
  import RefreshCw from "@lucide/svelte/icons/refresh-cw";
  import { SelectDropdown } from "@middleman/ui";
  import Modal from "./DocsModal.svelte";
  import type { DocsAPI } from "../../api/docs/api";
  import type { BrowseEntry, DocsAPIError, Folder } from "../../api/docs/types";
  import { getKataDaemonRoster } from "../../stores/active-kata-daemon.svelte";

  // Add-folder dialog with a built-in folder picker. The picker drives a
  // hidden `path` text field — users can also type the path manually if
  // they know it. Name and id default to the folder basename on the
  // server when omitted, so the optional fields stay collapsed by default.

  interface Props {
    open: boolean;
    api: DocsAPI;
    onClose: () => void;
    onAdded: (folder: Folder) => void;
    // Optional initial path. When set, the browser opens at that
    // directory; used by tests and for "edit path" reuse later.
    initialPath?: string;
  }

  let { open, api, onClose, onAdded, initialPath = "" }: Props = $props();

  // Form state. path holds the absolute folder the user wants to add;
  // browsePath tracks where we're navigating in the picker (which may
  // differ when the user types into the path input directly).
  let path = $state("");
  let name = $state("");
  let id = $state("");
  let daemon = $state("");
  let showAdvanced = $state(false);
  let daemonRoster = $derived(getKataDaemonRoster());
  let daemonOptions = $derived([
    { value: "", label: "Follow active daemon" },
    ...daemonRoster.map((daemonID) => ({ value: daemonID, label: daemonID })),
  ]);

  let browsePath = $state("");
  let entries = $state<BrowseEntry[]>([]);
  let parent = $state<string>("");
  let showHidden = $state(false);
  let loadingBrowse = $state(false);
  let browseError = $state<string | null>(null);

  let error = $state<string | null>(null);
  let saving = $state(false);

  // Sequence number guards stale async results: if the user clicks
  // through folders quickly, only the latest fetch's response wins.
  let browseSeq = 0;

  // Open / re-open seeds the browser once. Closing resets local state
  // so the next open starts fresh.
  $effect(() => {
    if (open) {
      path = initialPath;
      name = "";
      id = "";
      daemon = "";
      showAdvanced = false;
      error = null;
      saving = false;
      void loadBrowse(initialPath);
    } else {
      browsePath = "";
      entries = [];
      parent = "";
      browseError = null;
    }
  });

  async function loadBrowse(target: string) {
    const seq = ++browseSeq;
    loadingBrowse = true;
    browseError = null;
    try {
      const result = await api.browseDirectories(target || undefined);
      if (seq !== browseSeq) return;
      browsePath = result.path;
      parent = result.parent;
      entries = result.entries;
    } catch (err) {
      if (seq !== browseSeq) return;
      browseError = describeError(err, "Could not list folder");
    } finally {
      if (seq === browseSeq) loadingBrowse = false;
    }
  }

  function navigateInto(entry: BrowseEntry) {
    void loadBrowse(entry.path);
  }

  function navigateUp() {
    if (!parent) return;
    void loadBrowse(parent);
  }

  function useCurrentFolder() {
    path = browsePath;
  }

  function selectEntry(entry: BrowseEntry) {
    path = entry.path;
  }

  let visibleEntries = $derived(
    showHidden ? entries : entries.filter((e) => !e.hidden),
  );
  let hiddenCount = $derived(entries.filter((e) => e.hidden).length);

  async function submit() {
    if (saving) return;
    const trimmed = path.trim();
    if (!trimmed) {
      error = "Pick a folder or enter a path.";
      return;
    }
    error = null;
    saving = true;
    try {
      const selectedDaemon = daemonRoster.length > 1 ? daemon.trim() : "";
      const folder = await api.addFolder({
        path: trimmed,
        ...(name.trim() ? { name: name.trim() } : {}),
        ...(id.trim() ? { id: id.trim() } : {}),
        ...(selectedDaemon ? { daemon: selectedDaemon } : {}),
      });
      onAdded(folder);
      onClose();
    } catch (err) {
      error = describeError(err, "Could not add folder");
    } finally {
      saving = false;
    }
  }

  function describeError(err: unknown, fallback: string): string {
    if (err && typeof err === "object" && "message" in err) {
      const msg = (err as DocsAPIError).message;
      return msg ? msg : fallback;
    }
    return fallback;
  }

  function refresh() {
    void loadBrowse(browsePath);
  }
</script>

<Modal {open} title="Add folder" width={520} {onClose}>
  <form
    class="modal-form"
    onsubmit={(event) => {
      event.preventDefault();
      void submit();
    }}
  >
    <label class="modal-field">
      <span>Folder path</span>
      <input
        type="text"
        bind:value={path}
        placeholder="~/Notes"
        disabled={saving}
      />
    </label>

    {#if daemonRoster.length > 1}
      <label class="modal-field">
        <span>Daemon</span>
        <SelectDropdown
          title="Daemon"
          value={daemon}
          options={daemonOptions}
          onchange={(value) => { daemon = value; }}
          disabled={saving}
        />
      </label>
    {/if}

    <div class="picker" aria-label="Folder browser">
      <div class="picker-head">
        <button
          type="button"
          class="picker-btn"
          onclick={navigateUp}
          disabled={!parent || loadingBrowse}
          aria-label="Go up"
          title="Go up"
        >
          <ArrowUp size={13} strokeWidth={2} />
        </button>
        <span class="picker-path" title={browsePath}>
          {browsePath || "Loading…"}
        </span>
        <button
          type="button"
          class="picker-btn"
          onclick={refresh}
          disabled={loadingBrowse}
          aria-label="Refresh"
          title="Refresh"
        >
          <RefreshCw size={13} strokeWidth={2} />
        </button>
        <button
          type="button"
          class="picker-use"
          onclick={useCurrentFolder}
          disabled={loadingBrowse || !browsePath}
        >
          Use this folder
        </button>
      </div>

      <ul class="picker-list" role="listbox" aria-label="Subfolders">
        {#if browseError}
          <li class="picker-msg error">{browseError}</li>
        {:else if loadingBrowse && entries.length === 0}
          <li class="picker-msg muted">Loading…</li>
        {:else if visibleEntries.length === 0}
          <li class="picker-msg muted">No subfolders here.</li>
        {:else}
          {#each visibleEntries as entry (entry.path)}
            <li>
              <button
                type="button"
                class="picker-row"
                class:selected={entry.path === path}
                onclick={() => selectEntry(entry)}
                ondblclick={() => navigateInto(entry)}
              >
                <FolderIcon size={13} strokeWidth={1.75} />
                <span class="picker-row-name" class:hidden={entry.hidden}>
                  {entry.name}
                </span>
                <span
                  class="picker-row-open"
                  role="presentation"
                  onclick={(event) => {
                    event.stopPropagation();
                    navigateInto(entry);
                  }}
                  onkeydown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      navigateInto(entry);
                    }
                  }}
                  aria-label={`Open ${entry.name}`}
                  title="Open"
                  tabindex="-1"
                >
                  <FolderOpen size={13} strokeWidth={1.75} />
                </span>
              </button>
            </li>
          {/each}
        {/if}
      </ul>

      {#if hiddenCount > 0}
        <label class="picker-hidden-toggle">
          <input type="checkbox" bind:checked={showHidden} />
          <span>Show hidden ({hiddenCount})</span>
        </label>
      {/if}
    </div>

    <button
      type="button"
      class="advanced-toggle"
      onclick={() => (showAdvanced = !showAdvanced)}
      aria-expanded={showAdvanced}
    >
      {showAdvanced ? "Hide" : "Show"} advanced options
    </button>

    {#if showAdvanced}
      <label class="modal-field">
        <span>Display name (optional)</span>
        <input
          type="text"
          bind:value={name}
          placeholder="(defaults to folder name)"
          disabled={saving}
        />
      </label>
      <label class="modal-field">
        <span>Folder id (optional)</span>
        <input
          type="text"
          bind:value={id}
          placeholder="(defaults to folder name, lowercased)"
          disabled={saving}
        />
        <small class="modal-hint">
          Used in URLs. Must be unique. Stick to letters, numbers, and dashes.
        </small>
      </label>
    {/if}

    {#if error}
      <p class="modal-error" role="alert">{error}</p>
    {/if}

    <div class="modal-actions">
      <button type="button" class="toolbar-btn" onclick={onClose} disabled={saving}>
        Cancel
      </button>
      <button type="submit" class="toolbar-btn primary" disabled={saving || !path.trim()}>
        {saving ? "Adding…" : "Add folder"}
      </button>
    </div>
  </form>
</Modal>

<style>
  .modal-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .modal-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .modal-field span {
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .modal-field input {
    width: 100%;
    padding: 6px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-input);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .modal-field input:focus {
    outline: none;
    border-color: var(--border-focus);
  }

  .modal-field :global(.select-dropdown) {
    width: 100%;
    min-width: 0;
  }

  .modal-field :global(.select-dropdown-trigger) {
    height: 32px;
    font-size: var(--font-size-sm);
    font-weight: 400;
  }

  .picker {
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-input);
    overflow: hidden;
  }

  .picker-head {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 8px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .picker-path {
    flex: 1;
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-xs);
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    direction: rtl;
    text-align: left;
  }

  .picker-btn {
    width: 22px;
    height: 22px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    border: none;
    background: none;
    cursor: pointer;
  }

  .picker-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .picker-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .picker-use {
    padding: 3px 8px;
    font-size: var(--font-size-xs);
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    cursor: pointer;
  }

  .picker-use:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .picker-use:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .picker-list {
    max-height: 220px;
    overflow-y: auto;
    list-style: none;
    margin: 0;
    padding: 4px 0;
  }

  .picker-row {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border: none;
    background: none;
    text-align: left;
    cursor: pointer;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .picker-row:hover {
    background: var(--bg-surface-hover);
  }

  .picker-row.selected {
    background: var(--bg-surface-active, var(--bg-surface-hover));
  }

  .picker-row-name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .picker-row-name.hidden {
    color: var(--text-muted);
    font-style: italic;
  }

  .picker-row-open {
    width: 22px;
    height: 22px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
  }

  .picker-row-open:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .picker-msg {
    padding: 8px 10px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    list-style: none;
  }

  .picker-msg.error {
    color: var(--text-error, #cf222e);
  }

  .picker-hidden-toggle {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 10px;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    border-top: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .advanced-toggle {
    align-self: flex-start;
    border: none;
    background: none;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    cursor: pointer;
    padding: 2px 0;
  }

  .advanced-toggle:hover {
    color: var(--text-primary);
  }

  .modal-hint {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .modal-error {
    margin: 0;
    padding: 6px 8px;
    background: var(--bg-error-subtle, #ffebe9);
    color: var(--text-error, #cf222e);
    border-radius: var(--radius-sm);
    font-size: var(--font-size-xs);
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }
</style>
