<script lang="ts">
  import { SelectDropdown, type SelectDropdownOption } from "@middleman/ui";
  import type { RepoImportRow, SortState, StatusFilter } from "./repoImportSelection.js";
  import { rowKey } from "./repoImportSelection.js";

  const statusFilterOptions: SelectDropdownOption[] = [
    { value: "all", label: "All rows" },
    { value: "selected", label: "Selected" },
    { value: "unselected", label: "Unselected" },
    { value: "already-added", label: "Already added" },
  ];

  interface Props {
    rows: RepoImportRow[];
    selected: Set<string>;
    filterText: string;
    statusFilter: StatusFilter;
    hideForks: boolean;
    hidePrivate: boolean;
    sort: SortState;
    onFilterText: (value: string) => void;
    onStatusFilter: (value: StatusFilter) => void;
    onHideForks: (value: boolean) => void;
    onHidePrivate: (value: boolean) => void;
    onSort: (field: SortState["field"]) => void;
    onToggle: (row: RepoImportRow, checked: boolean, shiftKey: boolean) => void;
    onSelectVisible: () => void;
    onDeselectVisible: () => void;
  }

  let {
    rows,
    selected,
    filterText,
    statusFilter,
    hideForks,
    hidePrivate,
    sort,
    onFilterText,
    onStatusFilter,
    onHideForks,
    onHidePrivate,
    onSort,
    onToggle,
    onSelectVisible,
    onDeselectVisible,
  }: Props = $props();

  function sortLabel(field: SortState["field"]): string {
    if (sort.field !== field) return "";
    return sort.direction === "asc" ? " ↑" : " ↓";
  }

  function formatPushedAt(value: string | null): string {
    if (!value) return "Never pushed";
    return new Date(value).toLocaleString();
  }

  function repoLabel(row: RepoImportRow): string {
    return row.repo_path || `${row.owner}/${row.name}`;
  }

  function ariaSort(field: SortState["field"]): "ascending" | "descending" | "none" {
    if (sort.field !== field) return "none";
    return sort.direction === "asc" ? "ascending" : "descending";
  }
</script>

<div class="repo-preview-controls">
  <input
    class="filter-input"
    type="text"
    aria-label="Filter repositories"
    placeholder="Filter by name or description…"
    value={filterText}
    oninput={(event) => onFilterText(event.currentTarget.value)}
  />
  <SelectDropdown
    title="Repository status filter"
    value={statusFilter}
    options={statusFilterOptions}
    onchange={(value) => onStatusFilter(value as StatusFilter)}
  />
  <label class="toggle-filter">
    <input
      type="checkbox"
      checked={hideForks}
      onchange={(event) => onHideForks(event.currentTarget.checked)}
    />
    <span>Hide forks</span>
  </label>
  <label class="toggle-filter">
    <input
      type="checkbox"
      checked={hidePrivate}
      onchange={(event) => onHidePrivate(event.currentTarget.checked)}
    />
    <span>Hide private</span>
  </label>
  <button type="button" class="shortcut-btn" onclick={onSelectVisible}>All</button>
  <button type="button" class="shortcut-btn" onclick={onDeselectVisible}>None</button>
</div>

<div class="table-wrap">
  <table class="repo-preview-table">
    <thead>
      <tr>
        <th scope="col" class="select-col">Select</th>
        <th scope="col" aria-sort={ariaSort("name")}><button type="button" class="sort-btn" onclick={() => onSort("name")}>Repository{sortLabel("name")}</button></th>
        <th scope="col">Description</th>
        <th scope="col" aria-sort={ariaSort("pushed_at")}><button type="button" class="sort-btn" onclick={() => onSort("pushed_at")}>Last pushed{sortLabel("pushed_at")}</button></th>
        <th scope="col">Visibility</th>
        <th scope="col">Status</th>
      </tr>
    </thead>
    <tbody>
      {#each rows as row (rowKey(row))}
        {@const key = rowKey(row)}
        <tr class={[row.already_configured && "disabled-row"]}>
          <td>
            <input
              type="checkbox"
              aria-label={`Select ${repoLabel(row)}`}
              checked={selected.has(key)}
              disabled={row.already_configured}
              onclick={(event) => onToggle(row, event.currentTarget.checked, event.shiftKey)}
            />
          </td>
          <td class="repo-name">{repoLabel(row)}</td>
          <td class="description">{row.description ?? ""}</td>
          <td>{formatPushedAt(row.pushed_at)}</td>
          <td>
            <span class="chip chip-muted">{row.private ? "Private" : "Public"}</span>
            {#if row.fork}<span class="chip chip-muted">Fork</span>{/if}
          </td>
          <td>{#if row.already_configured}<span class="chip chip-amber">Already added</span>{/if}</td>
        </tr>
      {:else}
        <tr><td colspan="6" class="empty-cell">No repositories match current filters.</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .repo-preview-controls { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .filter-input { flex: 1; min-width: 220px; font-size: var(--font-size-md); padding: 6px 10px; background: var(--bg-inset); border: 1px solid var(--border-muted); border-radius: var(--radius-sm); }
  .toggle-filter { display: inline-flex; align-items: center; gap: 5px; font-size: var(--font-size-sm); color: var(--text-secondary); white-space: nowrap; }
  .toggle-filter input { margin: 0; }
  .shortcut-btn, .sort-btn { font-size: var(--font-size-sm); color: var(--accent-blue); }
  .table-wrap { overflow: auto; border: 1px solid var(--border-muted); border-radius: var(--radius-md); }
  .repo-preview-table { width: 100%; border-collapse: collapse; font-size: var(--font-size-sm); }
  th, td { padding: 8px 10px; border-bottom: 1px solid var(--border-muted); text-align: left; vertical-align: middle; }
  th { color: var(--text-muted); font-weight: 600; background: var(--bg-inset); }
  tr:last-child td { border-bottom: none; }
  .select-col { width: 52px; }
  .repo-name { font-weight: 600; color: var(--text-primary); white-space: nowrap; }
  .description { color: var(--text-secondary); min-width: 180px; }
  .disabled-row { opacity: 0.72; }
  .empty-cell { text-align: center; color: var(--text-muted); padding: 24px; }
  .chip { box-sizing: border-box; display: inline-flex; align-items: center; justify-content: center; min-height: 18px; margin-right: 4px; padding: 0 6px; border-radius: 9px; font-size: var(--font-size-2xs); font-weight: 600; line-height: 1; letter-spacing: 0.03em; text-transform: uppercase; white-space: nowrap; }
  .chip-muted { background: var(--bg-inset); color: var(--text-muted); }
  .chip-amber { background: color-mix(in srgb, var(--accent-amber) 15%, transparent); color: var(--accent-amber); }
</style>
