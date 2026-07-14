<script lang="ts">
  import { Button, Card, Checkbox, Chip, SearchInput, Table, TableHeaderCell } from "@kenn-io/kit-ui";
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

  let rangeShiftKey = false;
</script>

<div class="repo-preview-controls">
  <SearchInput
    class="filter-input"
    block
    ariaLabel="Filter repositories"
    placeholder="Filter by name or description…"
    value={filterText}
    oninput={onFilterText}
  />
  <SelectDropdown
    title="Repository status filter"
    value={statusFilter}
    options={statusFilterOptions}
    onchange={(value) => onStatusFilter(value as StatusFilter)}
  />
  <Checkbox
    class="toggle-filter"
    checked={hideForks}
    label="Hide forks"
    onchange={onHideForks}
  />
  <Checkbox
    class="toggle-filter"
    checked={hidePrivate}
    label="Hide private"
    onchange={onHidePrivate}
  />
  <Button size="sm" surface="soft" tone="info" onclick={onSelectVisible}>All</Button>
  <Button size="sm" surface="soft" onclick={onDeselectVisible}>None</Button>
</div>

<Card level="default" padding="none" class="table-wrap">
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
        <td
          onpointerdown={(event) => { rangeShiftKey = event.shiftKey; }}
          onkeydown={(event) => { rangeShiftKey = event.shiftKey; }}
        >
          <Checkbox
            checked={selected.has(key)}
            disabled={row.already_configured}
            ariaLabel={`Select ${repoLabel(row)}`}
            onchange={(checked) => {
              onToggle(row, checked, rangeShiftKey);
              rangeShiftKey = false;
            }}
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
</Card>

<style>
  .repo-preview-controls { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  :global(.filter-input) { flex: 1; min-width: 220px; }
  :global(.toggle-filter) { white-space: nowrap; }
  :global(.toggle-filter .kit-checkbox__label) { color: var(--text-secondary); }
  :global(.table-wrap) { overflow: auto; }
  :global(.table-wrap .kit-table) { font-size: var(--font-size-sm); }
  :global(.table-wrap th.select-col) { width: 52px; }
  td { padding: 8px 10px; border-bottom: 1px solid var(--border-muted); text-align: left; vertical-align: middle; }
  tr:last-child td { border-bottom: none; }
  .repo-name { font-weight: 600; color: var(--text-primary); white-space: nowrap; }
  .description { color: var(--text-secondary); min-width: 180px; }
  .disabled-row { opacity: 0.72; }
  .empty-cell { text-align: center; color: var(--text-muted); padding: 24px; }
  td :global(.kit-chip) { margin-right: 4px; }
</style>
