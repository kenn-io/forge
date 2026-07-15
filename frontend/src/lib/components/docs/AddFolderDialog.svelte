<script lang="ts">
  import ArrowUp from "@lucide/svelte/icons/arrow-up";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import FolderOpen from "@lucide/svelte/icons/folder-open";
  import RefreshCw from "@lucide/svelte/icons/refresh-cw";
  import { Button, Card, Checkbox, IconButton, TextInput } from "@kenn-io/kit-ui";
  import { SelectDropdown } from "@middleman/ui";
  import { showFlash } from "@middleman/ui/stores/flash";
  import Modal from "../shared/Modal.svelte";
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
      const message = describeError(err, "Could not add folder");
      if (["already_exists", "duplicate_folder_id"].includes((err as DocsAPIError | undefined)?.code ?? "")) {
        error = message;
      } else {
        showFlash(message, { tone: "danger" });
      }
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
      <TextInput
        bind:value={path}
        block
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

    <Card class="docs-folder-picker" level="inset" padding="none" ariaLabel="Folder browser">
      <div class="picker-head">
        <IconButton
          size="sm"
          onclick={navigateUp}
          disabled={!parent || loadingBrowse}
          ariaLabel="Go up"
        >
          <ArrowUp size={13} strokeWidth={2} />
        </IconButton>
        <span class="picker-path" title={browsePath}>
          {browsePath || "Loading…"}
        </span>
        <IconButton
          size="sm"
          onclick={refresh}
          disabled={loadingBrowse}
          ariaLabel="Refresh"
        >
          <RefreshCw size={13} strokeWidth={2} />
        </IconButton>
        <Button
          size="sm"
          onclick={useCurrentFolder}
          disabled={loadingBrowse || !browsePath}
        >
          Use this folder
        </Button>
      </div>

      <ul class="picker-list" aria-label="Subfolders">
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
        <Checkbox
          class="picker-hidden-toggle"
          bind:checked={showHidden}
          label={`Show hidden (${hiddenCount})`}
        />
      {/if}
    </Card>

    <Button
      class="advanced-toggle"
      size="sm"
      surface="soft"
      onclick={() => (showAdvanced = !showAdvanced)}
      ariaExpanded={showAdvanced}
    >
      {showAdvanced ? "Hide" : "Show"} advanced options
    </Button>

    {#if showAdvanced}
      <label class="modal-field">
        <span>Display name (optional)</span>
        <TextInput
          bind:value={name}
          block
          placeholder="(defaults to folder name)"
          disabled={saving}
        />
      </label>
      <label class="modal-field">
        <span>Folder id (optional)</span>
        <TextInput
          bind:value={id}
          block
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
      <Button onclick={onClose} disabled={saving}>Cancel</Button>
      <Button type="submit" tone="info" surface="solid" disabled={saving || !path.trim()}>
        {saving ? "Adding…" : "Add folder"}
      </Button>
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

  .modal-field :global(.kit-select-dropdown) {
    width: 100%;
    min-width: 0;
  }

  .modal-field :global(.kit-select-dropdown__trigger) {
    height: 32px;
    font-size: var(--font-size-sm);
    font-weight: 400;
  }

  :global(.docs-folder-picker) {
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

  :global(.picker-hidden-toggle) {
    width: 100%;
    box-sizing: border-box;
    padding: 6px 10px;
    border-top: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  :global(.picker-hidden-toggle .kit-checkbox__label) {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  :global(.advanced-toggle) {
    align-self: flex-start;
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
