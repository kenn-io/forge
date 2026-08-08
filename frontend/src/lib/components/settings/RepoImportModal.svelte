<script lang="ts">
  import { Button, SelectDropdown, type SelectDropdownOption } from "@kenn-io/kit-ui";
  import { TextInput } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { onDestroy } from "svelte";
  import type { Settings } from "../../api/types.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import {
    SettingsWorkflow,
    settingsErrorMessage,
    type RepoPreviewRow,
  } from "../../stores/settings-workflow.js";
  import Modal from "../shared/Modal.svelte";
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
  const runtime = getAppRuntime();

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
  let previewExecution: AppExecution<void, never> | null = null;

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

  onDestroy(() => previewExecution?.interrupt());

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

  function cancelPreview(): void {
    previewExecution?.interrupt();
    previewExecution = null;
    loading = false;
  }

  function resetAll(): void {
    cancelPreview();
    patternInput = "";
    provider = defaultRepoImportProvider.id;
    hostInput = defaultRepoImportProvider.defaultHost;
    resetPreviewState();
    error = null;
    submitting = false;
  }

  function handlePatternInput(value: string): void {
    cancelPreview();
    patternInput = value;
    resetPreviewState();
    error = null;
  }

  function handleProviderChange(value: string): void {
    cancelPreview();
    provider = value;
    hostInput = repoImportProvider(value).defaultHost;
    resetPreviewState();
    error = null;
  }

  function handleHostInput(value: string): void {
    cancelPreview();
    hostInput = value;
    resetPreviewState();
    error = null;
  }

  function handlePreview(): void {
    if (loading) return;
    let parsed: { owner: string; pattern: string };
    try {
      parsed = parseImportPattern(patternInput, providerMeta.allowNestedOwner);
    } catch (err) {
      resetPreviewState();
      error = err instanceof Error ? err.message : String(err);
      return;
    }
    loading = true;
    error = null;
    resetPreviewState();
    const options = { provider, host: hostInput.trim() };
    let execution: AppExecution<void, never> | undefined;
    const program = Effect.gen(function* () {
      const workflow = yield* SettingsWorkflow;
      return yield* workflow.previewRepos(parsed.owner, parsed.pattern, options);
    }).pipe(
      Effect.matchEffect({
        onFailure: (failure) => Effect.sync(() => {
          if (execution === undefined || previewExecution !== execution) return;
          resetPreviewState();
          error = settingsErrorMessage(failure);
        }),
        onSuccess: (response) => Effect.sync(() => {
          if (execution === undefined || previewExecution !== execution) return;
          rows = response.repos;
          selected = new Set(response.repos.filter((row) => !row.already_configured).map(rowKey));
        }),
      }),
      Effect.ensuring(Effect.sync(() => {
        if (execution === undefined || previewExecution !== execution) return;
        previewExecution = null;
        loading = false;
      })),
    );
    execution = runtime.runCommand(program, {
      operation: "preview repositories",
      safeContext: { provider: options.provider, host: options.host },
      onFailure: () => {},
    });
    previewExecution = execution;
  }

  function handleSubmit(): void {
    if (submitRows.length === 0) return;
    submitting = true;
    error = null;
    const repos = submitRows.map((row) => ({
      provider: row.provider,
      host: row.platform_host,
      owner: row.owner,
      name: row.name,
      repo_path: row.repo_path,
    }));
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.bulkAddRepos(repos);
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => showFlash(settingsErrorMessage(failure), { tone: "danger" })),
          onSuccess: (settings) => Effect.sync(() => {
            onImported(settings);
            onClose();
          }),
        }),
        Effect.ensuring(Effect.sync(() => {
          submitting = false;
        })),
      ),
      {
        operation: "add selected repositories",
        safeContext: { count: repos.length },
        onFailure: () => {},
      },
    );
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
          <TextInput
            block
            value={hostInput}
            placeholder={providerMeta.defaultHost}
            oninput={handleHostInput}
          />
        </label>
        <label>
          <span>Repository pattern</span>
          <TextInput
            block
            autofocus
            value={patternInput}
            placeholder={providerMeta.ownerPatternPlaceholder}
            oninput={handlePatternInput}
            onkeydown={(event) => { if (event.key === "Enter" && !loading) handlePreview(); }}
          />
        </label>
        <Button
          tone="info"
          surface="solid"
          onclick={handlePreview}
          disabled={loading || !patternInput.trim()}
        >
          {loading ? "Previewing…" : "Preview"}
        </Button>
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
      <Button onclick={closeIfAllowed} disabled={submitting}>Cancel</Button>
      <Button
        tone="info"
        surface="solid"
        onclick={handleSubmit}
        disabled={submitting || selectedCount === 0}
      >
        {submitting ? "Adding…" : "Add selected repositories"}
      </Button>
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
  .provider-field :global(.provider-select) { width: 100%; min-width: 0; }
  .provider-field :global(.kit-select-dropdown__trigger) { height: 28px; font-size: var(--font-size-sm); font-weight: 400; }
  .error-msg { color: var(--accent-red); font-size: var(--font-size-sm); }
  .empty-preview { border: 1px dashed var(--border-muted); border-radius: var(--radius-md); padding: 28px; color: var(--text-muted); text-align: center; font-size: var(--font-size-md); }
  .footer-actions { display: flex; gap: 8px; }
</style>
