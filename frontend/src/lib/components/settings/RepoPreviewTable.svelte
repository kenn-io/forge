<script lang="ts">
  import { Chip, Table, TableHeaderCell } from "@kenn-io/kit-ui";
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

  function formatPushedAt(value: string | null): string {
    if (!value) return "Never pushed";
    return new Date(value).toLocaleString();
  }

  function repoLabel(row: RepoImportRow): string {
    return row.repo_path || `${row.owner}/${row.name}`;
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
  <Table ariaLabel="Repository import preview" zebra={false} stickyHeader={false}>
    {#snippet header()}
      <TableHeaderCell label="Select" class="select-col" />
      <TableHeaderCell
        label="Repository"
        sortable
        sortDirection={sort.field === "name" ? sort.direction : null}
        onsort={() => onSort("name")}
      />
      <TableHeaderCell label="Description" />
      <TableHeaderCell
        label="Last pushed"
        sortable
        sortDirection={sort.field === "pushed_at" ? sort.direction : null}
        onsort={() => onSort("pushed_at")}
      />
      <TableHeaderCell label="Visibility" />
      <TableHeaderCell label="Status" />
    {/snippet}
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
          <Chip size="xs" tone="muted">{row.private ? "Private" : "Public"}</Chip>
          {#if row.fork}<Chip size="xs" tone="muted">Fork</Chip>{/if}
        </td>
        <td>{#if row.already_configured}<Chip size="xs" tone="warning">Already added</Chip>{/if}</td>
      </tr>
    {:else}
      <tr><td colspan="6" class="empty-cell">No repositories match current filters.</td></tr>
    {/each}
  </Table>
</div>

<style>
  .repo-preview-controls { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .filter-input { flex: 1; min-width: 220px; font-size: var(--font-size-md); padding: 6px 10px; background: var(--bg-inset); border: 1px solid var(--border-muted); border-radius: var(--radius-sm); }
  .toggle-filter { display: inline-flex; align-items: center; gap: var(--space-2); font-size: var(--font-size-sm); color: var(--text-secondary); white-space: nowrap; }
  .toggle-filter input { margin: 0; }
  .shortcut-btn { font-size: var(--font-size-sm); color: var(--accent-blue); }
  .table-wrap { overflow: auto; border: 1px solid var(--border-muted); border-radius: var(--radius-md); }
  .table-wrap :global(.kit-table) { font-size: var(--font-size-sm); }
  .table-wrap :global(th.select-col) { width: 52px; }
  td { padding: 8px 10px; border-bottom: 1px solid var(--border-muted); text-align: left; vertical-align: middle; }
  tr:last-child td { border-bottom: none; }
  .repo-name { font-weight: 600; color: var(--text-primary); white-space: nowrap; }
  .description { color: var(--text-secondary); min-width: 180px; }
  .disabled-row { opacity: 0.72; }
  .empty-cell { text-align: center; color: var(--text-muted); padding: 24px; }
  td :global(.kit-chip) { margin-right: 4px; }
</style>
