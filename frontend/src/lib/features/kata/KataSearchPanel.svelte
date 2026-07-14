<script lang="ts">
  import { SearchInput, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import { SelectDropdown } from "@middleman/ui";

  import type {
    KataDuplicateCandidateDisplay,
    KataProjectSummary,
    KataTaskSearchFilters,
  } from "../../api/kata/taskTypes.js";

  interface Props {
    filters: KataTaskSearchFilters;
    projects: KataProjectSummary[];
    duplicateCandidates?: KataDuplicateCandidateDisplay[] | undefined;
    onChange: (filters: KataTaskSearchFilters) => void | Promise<void>;
  }

  let { filters, projects, duplicateCandidates = [], onChange }: Props = $props();
  let draftOverride = $state<KataTaskSearchFilters | null>(null);
  let draft = $derived(draftOverride ?? filters);
  let lastFilters: KataTaskSearchFilters | null = null;

  $effect(() => {
    if (filters !== lastFilters) {
      lastFilters = filters;
      draftOverride = null;
    }
  });

  const statusOptions = [
    { value: "open", label: "Open" },
    { value: "closed", label: "Closed" },
    { value: "all", label: "All" },
  ];
  const projectOptions = $derived.by<TypeaheadOption[]>(() =>
    projects
      .map((project) => ({
        name: project.uid,
        label: project.name,
        meta: String(project.open_count),
      }))
      .sort((a, b) => a.label.localeCompare(b.label, undefined, { sensitivity: "base" })),
  );

  function emit(next: Partial<KataTaskSearchFilters>): void {
    const nextFilters = {
      ...draft,
      ...next,
      scope: next.scope ?? draft.scope,
    };
    draftOverride = nextFilters;
    void onChange(nextFilters);
  }

  function inputValue(event: Event): string {
    const target = event.currentTarget;
    if (target instanceof HTMLInputElement || target instanceof HTMLSelectElement) return target.value;
    return "";
  }
</script>

<section class="kata-search-panel" aria-label="Search and filters">
  <div class="kata-search-toolbar">
    <div class="query-field">
      <SearchInput
        value={draft.query}
        size="sm"
        block
        placeholder="Search tasks..."
        ariaLabel="Search tasks"
        oninput={(query) => emit({ query })}
      />
    </div>

    <div class="filter-control filter-control-project">
      <span class="kit-sr-only">Project scope</span>
      <Typeahead
        options={projectOptions}
        value={draft.scope.kind === "project" ? draft.scope.project_uid : ""}
        fallbackLabel="All projects"
        placeholder="Project scope"
        triggerPrefix="Project scope:"
        allowClear
        clearLabel="All projects"
        emptyLabel="No matching projects"
        onselect={(value) => {
          emit({ scope: value === "" ? { kind: "all" } : { kind: "project", project_uid: value } });
        }}
      />
    </div>

    <div class="filter-control filter-control-status">
      <span class="kit-sr-only">Status</span>
      <SelectDropdown
        title="Status"
        value={draft.status}
        options={statusOptions}
        onchange={(value) => emit({ status: value as KataTaskSearchFilters["status"] })}
      />
    </div>

    <label class="filter-control filter-control-input">
      <span class="kit-sr-only">Owner</span>
      <input
        aria-label="Owner"
        value={draft.owner}
        placeholder="Owner"
        oninput={(event) => emit({ owner: inputValue(event) })}
        onchange={(event) => emit({ owner: inputValue(event) })}
      />
    </label>

    <label class="filter-control filter-control-input">
      <span class="kit-sr-only">Label</span>
      <input
        aria-label="Label"
        value={draft.label}
        placeholder="Label"
        oninput={(event) => emit({ label: inputValue(event) })}
        onchange={(event) => emit({ label: inputValue(event) })}
      />
    </label>
  </div>

  {#if duplicateCandidates.length > 0}
    <ul class="duplicate-list" aria-label="Duplicate candidates">
      {#each duplicateCandidates as candidate (`${candidate.qualified_id}:${candidate.title}`)}
        <li>
          <strong>{candidate.title}</strong>
          <span>{candidate.qualified_id}</span>
          {#if candidate.reason}
            <em>{candidate.reason}</em>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .kata-search-panel {
    padding: 7px 10px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .kata-search-toolbar {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }

  .query-field {
    flex: 1;
    min-width: 150px;
  }

  .filter-control {
    display: flex;
    align-items: center;
    min-width: 0;
  }

  input {
    box-sizing: border-box;
    min-width: 0;
    height: 28px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-xs);
    padding: 0 6px;
  }

  input:focus {
    outline: 2px solid var(--accent-blue);
    outline-offset: -1px;
  }

  .filter-control-project :global(.kit-typeahead) {
    width: 168px;
  }

  .filter-control-project :global(.kit-typeahead__prefix) {
    display: none;
  }

  .filter-control-project :global(.kit-typeahead__trigger),
  .filter-control-project :global(.kit-typeahead__input) {
    height: 28px;
    font-size: var(--font-size-xs);
    background: var(--bg-primary);
  }

  .filter-control-status :global(.kit-select-dropdown) {
    width: 102px;
  }

  .filter-control-input input {
    width: 92px;
  }

  .duplicate-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 8px 0 0;
    padding: 0;
    list-style: none;
  }

  .duplicate-list li {
    display: grid;
    grid-template-columns: minmax(120px, 1fr) auto minmax(80px, 0.8fr);
    gap: 8px;
    align-items: center;
    min-height: 28px;
    padding: 4px 8px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .duplicate-list strong {
    color: var(--text-primary);
    font-weight: 600;
    min-width: 0;
  }

  .duplicate-list span,
  .duplicate-list em {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .duplicate-list em {
    color: var(--text-muted);
    font-style: normal;
  }

  @media (max-width: 900px) {
    .kata-search-toolbar {
      flex-wrap: wrap;
    }

    .query-field {
      flex: 1 0 100%;
    }

    .duplicate-list li {
      grid-template-columns: 1fr;
      align-items: start;
    }
  }
</style>
