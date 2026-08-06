<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy } from "svelte";
  import { SearchInput, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import { SelectDropdown } from "@kenn-io/kit-ui";

  import type { KataProjectSummary, KataTaskSearchFilters } from "../../api/kata/taskTypes.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { KataCommand } from "./kata-command.js";

  interface Props {
    filters: KataTaskSearchFilters;
    projects: KataProjectSummary[];
    onChange: (filters: KataTaskSearchFilters) => KataCommand<boolean>;
  }

  let { filters, projects, onChange }: Props = $props();
  const runtime = getAppRuntime();
  let draftOverride = $state<KataTaskSearchFilters | null>(null);
  let draft = $derived(draftOverride ?? filters);
  let lastFilters: KataTaskSearchFilters | null = null;
  let changeExecution: AppExecution<void, never> | null = null;

  $effect(() => {
    if (filters !== lastFilters) {
      lastFilters = filters;
      draftOverride = null;
    }
  });

  const statusOptions = [
    { value: "open", label: "Open" },
    { value: "ready", label: "Ready" },
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
    changeExecution?.interrupt();
    changeExecution = runtime.runCommand(onChange(nextFilters).pipe(Effect.asVoid), {
      operation: "update Kata task filters",
      safeContext: {},
      onFailure: () => {},
    });
  }

  function inputValue(event: Event): string {
    const target = event.currentTarget;
    if (target instanceof HTMLInputElement || target instanceof HTMLSelectElement) return target.value;
    return "";
  }

  function updateStatus(value: string): void {
    if (value !== "open" && value !== "ready" && value !== "closed" && value !== "all") return;
    emit({ status: value });
  }

  onDestroy(() => changeExecution?.interrupt());
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
        onchange={updateStatus}
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

  @media (max-width: 900px) {
    .kata-search-toolbar {
      flex-wrap: wrap;
    }

    .query-field {
      flex: 1 0 100%;
    }

  }
</style>
