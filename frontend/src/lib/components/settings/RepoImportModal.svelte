<script lang="ts">
  import { SelectDropdown, type SelectDropdownOption } from "@middleman/ui";
  import type { Settings } from "@middleman/ui/api/types";
  import Modal from "../shared/Modal.svelte";
  import { bulkAddRepos, previewRepos, type RepoPreviewRow } from "../../api/settings.js";
  import RepoPreviewTable from "./RepoPreviewTable.svelte";
  import { defaultRepoImportProvider, repoImportProvider, repoImportProviders } from "./repoImportProviders.js";
  import {
    applyRangeSelection,
    filterRows,
    parseImportPattern,
    rowKey,
    selectedRowsForSubmit,
    setAllVisible,
    sortRows,
    type SortState,
    type StatusFilter,
  } from "./repoImportSelection.js";

  interface Props {
    open: boolean;
    onClose: () => void;
    onImported: (settings: Settings) => void;
  }

  let { open, onClose, onImported }: Props = $props();

  const providerOptions: SelectDropdownOption[] = repoImportProviders.map((option) => ({
    value: option.id,
    label: option.label,
  }));

  let patternInput = $state("");
  let provider = $state("github");
  let hostInput = $state("github.com");
  let rows = $state.raw<RepoPreviewRow[]>([]);
  let selected = $state<Set<string>>(new Set());
  let filterText = $state("");
  let statusFilter = $state<StatusFilter>("all");
  let hideForks = $state(false);
  let hidePrivate = $state(false);
  let sort = $state<SortState>({ field: "pushed_at", direction: "desc" });
  let anchorKey = $state<string | null>(null);
  let loading = $state(false);
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let requestToken = 0;

  const sortedRows = $derived(sortRows(rows, sort));
  const visibilityFilters = $derived({ hideForks, hidePrivate });
  const visibleRows = $derived(filterRows(sortedRows, filterText, statusFilter, selected, visibilityFilters));
  const selectableVisibleCount = $derived(visibleRows.filter((row) => !row.already_configured).length);
  const selectedCount = $derived(visibleRows.filter((row) => selected.has(rowKey(row)) && !row.already_configured).length);
  const submitRows = $derived(selectedRowsForSubmit(sortedRows, selected, visibilityFilters));
  const providerMeta = $derived(repoImportProvider(provider));

  $effect(() => {
    if (!open) resetAll();
  });

  function resetPreviewState(): void {
    rows = [];
    selected = new Set();
    filterText = "";
    statusFilter = "all";
    hideForks = false;
    hidePrivate = false;
    sort = { field: "pushed_at", direction: "desc" };
    anchorKey = null;
  }

  function resetAll(): void {
    patternInput = "";
    provider = defaultRepoImportProvider.id;
    hostInput = defaultRepoImportProvider.defaultHost;
    resetPreviewState();
    error = null;
    loading = false;
    submitting = false;
    requestToken += 1;
  }

  function handlePatternInput(value: string): void {
    patternInput = value;
    requestToken += 1;
    resetPreviewState();
    error = null;
    loading = false;
  }

  function handleProviderChange(value: string): void {
    provider = value;
    hostInput = repoImportProvider(value).defaultHost;
    requestToken += 1;
    resetPreviewState();
    error = null;
    loading = false;
  }

  async function handlePreview(): Promise<void> {
    if (loading) return;
    let parsed: { owner: string; pattern: string };
    try {
      parsed = parseImportPattern(patternInput, providerMeta.allowNestedOwner);
    } catch (err) {
      resetPreviewState();
      error = err instanceof Error ? err.message : String(err);
      return;
    }
    const token = ++requestToken;
    loading = true;
    error = null;
    resetPreviewState();
    try {
      const resp = await previewRepos(parsed.owner, parsed.pattern, {
        provider,
        host: hostInput.trim(),
      });
      if (token !== requestToken) return;
      rows = resp.repos;
      selected = new Set(resp.repos.filter((row) => !row.already_configured).map(rowKey));
    } catch (err) {
      if (token !== requestToken) return;
      resetPreviewState();
      error = err instanceof Error ? err.message : String(err);
    } finally {
      if (token === requestToken) loading = false;
    }
  }

  async function handleSubmit(): Promise<void> {
    if (submitRows.length === 0) return;
    submitting = true;
    error = null;
    try {
      const settings = await bulkAddRepos(submitRows.map((row) => ({
        provider: row.provider,
        host: row.platform_host,
        owner: row.owner,
        name: row.name,
        repo_path: row.repo_path,
      })));
      onImported(settings);
      onClose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      submitting = false;
    }
  }

  function toggleSort(field: SortState["field"]): void {
    sort = sort.field === field
      ? { field, direction: sort.direction === "asc" ? "desc" : "asc" }
      : { field, direction: field === "pushed_at" ? "desc" : "asc" };
  }

  function toggleRow(row: RepoPreviewRow, checked: boolean, shiftKey: boolean): void {
    const key = rowKey(row);
    if (shiftKey) {
      const result = applyRangeSelection({ selected, visibleRows, anchorKey, clickedKey: key, checked });
      selected = result.selected;
      anchorKey = result.anchorKey;
      return;
    }
    const next = new Set(selected);
    if (checked) next.add(key);
    else next.delete(key);
    selected = next;
    anchorKey = key;
  }

  function closeIfAllowed(): void {
    if (!submitting) onClose();
  }
</script>

<Modal
  {open}
  title="Add repositories"
  width={1040}
  frameId="repo-import-modal"
  showClose
  onClose={closeIfAllowed}
>
  <div class="import-content">
    <div class="preview-form">
        <label class="provider-field">
          <span>Provider</span>
          <SelectDropdown
            class="provider-select"
            title="Provider"
            value={provider}
            options={providerOptions}
            onchange={handleProviderChange}
          />
        </label>
        <label class="host-field">
          <span>Host</span>
          <input
            value={hostInput}
            placeholder={providerMeta.defaultHost}
            oninput={(event) => { hostInput = event.currentTarget.value; }}
          />
        </label>
        <label>
          <span>Repository pattern</span>
          <input
            data-autofocus
            value={patternInput}
            placeholder={providerMeta.ownerPatternPlaceholder}
            oninput={(event) => handlePatternInput(event.currentTarget.value)}
            onkeydown={(event) => { if (event.key === "Enter" && !loading) void handlePreview(); }}
          />
        </label>
        <button class="preview-btn" type="button" onclick={() => void handlePreview()} disabled={loading || !patternInput.trim()}>
          {loading ? "Previewing…" : "Preview"}
        </button>
      </div>

      {#if error}
        <div class="error-msg" role="alert">{error}</div>
      {/if}

      {#if rows.length > 0}
        <RepoPreviewTable
          rows={visibleRows}
          {selected}
          {filterText}
          {statusFilter}
          {hideForks}
          {hidePrivate}
          {sort}
          onFilterText={(value) => { filterText = value; }}
          onStatusFilter={(value) => { statusFilter = value; }}
          onHideForks={(value) => { hideForks = value; }}
          onHidePrivate={(value) => { hidePrivate = value; }}
          onSort={toggleSort}
          onToggle={toggleRow}
          onSelectVisible={() => { selected = setAllVisible(selected, visibleRows, true); }}
          onDeselectVisible={() => { selected = setAllVisible(selected, visibleRows, false); }}
        />
      {:else if !loading && !error}
        <div class="empty-preview">Preview repositories before adding them.</div>
      {/if}

  </div>
  {#snippet footer()}
    <span class="footer-status">Selected {selectedCount} of {selectableVisibleCount}</span>
    <div class="footer-actions">
      <button class="secondary-btn" type="button" onclick={closeIfAllowed} disabled={submitting}>Cancel</button>
      <button class="submit-btn" type="button" onclick={() => void handleSubmit()} disabled={submitting || selectedCount === 0}>
        {submitting ? "Adding…" : "Add selected repositories"}
      </button>
    </div>
  {/snippet}
</Modal>

<style>
  .import-content { display: flex; flex-direction: column; gap: var(--space-5); }
  .footer-status { margin-right: auto; color: var(--text-muted); font-size: var(--font-size-sm); }
  .preview-form { display: flex; gap: var(--space-4); align-items: end; }
  label { flex: 1; display: flex; flex-direction: column; gap: 6px; font-size: var(--font-size-sm); color: var(--text-secondary); }
  .provider-field { flex: 0 0 120px; }
  .host-field { flex: 0 0 190px; }
  input { font-size: var(--font-size-md); padding: 7px 10px; color: var(--text-primary); background: var(--bg-inset); border: 1px solid var(--border-muted); border-radius: var(--radius-sm); }
  .provider-field :global(.provider-select) { width: 100%; min-width: 0; }
  .provider-field :global(.kit-select-dropdown__trigger) { height: 34px; font-size: var(--font-size-md); font-weight: 400; }
  .preview-btn, .submit-btn { padding: 7px 14px; font-size: var(--font-size-md); font-weight: 600; color: white; background: var(--accent-blue); border-radius: var(--radius-sm); }
  .secondary-btn { padding: 7px 14px; font-size: var(--font-size-md); color: var(--text-secondary); background: var(--bg-inset); border: 1px solid var(--border-muted); border-radius: var(--radius-sm); }
  button:disabled { opacity: 0.5; cursor: not-allowed; }
  .error-msg { color: var(--accent-red); font-size: var(--font-size-sm); }
  .empty-preview { border: 1px dashed var(--border-muted); border-radius: var(--radius-md); padding: 28px; color: var(--text-muted); text-align: center; font-size: var(--font-size-md); }
  .footer-actions { display: flex; gap: 8px; }
</style>
