export type SortField = "name" | "pushed_at";
export type SortDirection = "asc" | "desc";
export type StatusFilter = "all" | "selected" | "unselected" | "already-added";

export interface RepoImportRow {
  provider: string;
  platform_host: string;
  owner: string;
  name: string;
  repo_path: string;
  description: string | null;
  private: boolean;
  fork: boolean;
  pushed_at: string | null;
  already_configured: boolean;
}

export interface SortState {
  field: SortField;
  direction: SortDirection;
}

export interface RepoVisibilityFilters {
  hideForks?: boolean;
  hidePrivate?: boolean;
}

export function rowKey(
  row: Pick<RepoImportRow, "provider" | "platform_host" | "repo_path"> & Partial<Pick<RepoImportRow, "owner" | "name">>,
): string {
  return `${row.provider.toLowerCase()}/${row.platform_host.toLowerCase()}/${row.repo_path.toLowerCase()}`;
}

export function parseImportPattern(input: string, allowNestedOwner = false): { owner: string; pattern: string } {
  const trimmed = input.trim();
  const parts = trimmed.split("/");
  if (parts.length < 2 || (!allowNestedOwner && parts.length !== 2)) {
    throw new Error("Format: owner/pattern");
  }
  const rawPattern = parts.at(-1) ?? "";
  const rawOwner = parts.slice(0, -1).join("/");
  const owner = rawOwner.trim();
  const pattern = rawPattern.trim();
  if (!owner) throw new Error("owner is required");
  if (!pattern) throw new Error("pattern is required");
  if (/[*?[\]]/.test(owner)) throw new Error("glob syntax in owner is not supported");
  if (pattern.includes("/")) throw new Error("pattern must not contain /");
  return { owner, pattern };
}

export function filterRows(
  rows: RepoImportRow[],
  query: string,
  status: StatusFilter,
  selected = new Set<string>(),
  visibilityFilters: RepoVisibilityFilters = {},
): RepoImportRow[] {
  const needle = query.trim().toLowerCase();
  return rows.filter((row) => {
    if (visibilityFilters.hidePrivate && row.private) return false;
    if (visibilityFilters.hideForks && row.fork) return false;
    const key = rowKey(row);
    const matchesText =
      needle === "" ||
      key.includes(needle) ||
      row.name.toLowerCase().includes(needle) ||
      (row.description ?? "").toLowerCase().includes(needle);
    if (!matchesText) return false;
    if (status === "selected") return selected.has(key);
    if (status === "unselected") return !row.already_configured && !selected.has(key);
    if (status === "already-added") return row.already_configured;
    return true;
  });
}

export function sortRows(rows: RepoImportRow[], sort: SortState): RepoImportRow[] {
  return rows
    .map((row, index) => ({ row, index }))
    .sort((left, right) => {
      let cmp: number;
      if (sort.field === "name") {
        cmp = rowKey(left.row).localeCompare(rowKey(right.row));
      } else {
        const leftTime = left.row.pushed_at ? Date.parse(left.row.pushed_at) : null;
        const rightTime = right.row.pushed_at ? Date.parse(right.row.pushed_at) : null;
        if (leftTime === null && rightTime === null) cmp = 0;
        else if (leftTime === null) cmp = 1;
        else if (rightTime === null) cmp = -1;
        else cmp = sort.direction === "desc" ? rightTime - leftTime : leftTime - rightTime;
      }
      if (sort.direction === "desc" && sort.field !== "pushed_at") cmp = -cmp;
      if (cmp !== 0) return cmp;
      const keyCmp = rowKey(left.row).localeCompare(rowKey(right.row));
      if (keyCmp !== 0) return keyCmp;
      return left.index - right.index;
    })
    .map(({ row }) => row);
}

export function setAllVisible(selected: Set<string>, visibleRows: RepoImportRow[], checked: boolean): Set<string> {
  const next = new Set(selected);
  for (const row of visibleRows) {
    if (row.already_configured) continue;
    const key = rowKey(row);
    if (checked) next.add(key);
    else next.delete(key);
  }
  return next;
}

export function applyRangeSelection(input: {
  selected: Set<string>;
  visibleRows: RepoImportRow[];
  anchorKey: string | null;
  clickedKey: string;
  checked: boolean;
}): { selected: Set<string>; anchorKey: string } {
  const next = new Set(input.selected);
  const clickedIndex = input.visibleRows.findIndex((row) => rowKey(row) === input.clickedKey);
  const anchorIndex = input.anchorKey ? input.visibleRows.findIndex((row) => rowKey(row) === input.anchorKey) : -1;
  if (clickedIndex === -1) return { selected: next, anchorKey: input.clickedKey };
  const start = anchorIndex === -1 ? clickedIndex : Math.min(anchorIndex, clickedIndex);
  const end = anchorIndex === -1 ? clickedIndex : Math.max(anchorIndex, clickedIndex);
  for (const row of input.visibleRows.slice(start, end + 1)) {
    if (row.already_configured) continue;
    const key = rowKey(row);
    if (input.checked) next.add(key);
    else next.delete(key);
  }
  return { selected: next, anchorKey: input.clickedKey };
}

export function selectedRowsForSubmit(
  sortedRows: RepoImportRow[],
  selected: Set<string>,
  visibilityFilters: RepoVisibilityFilters = {},
): RepoImportRow[] {
  return sortedRows.filter((row) => {
    if (visibilityFilters.hidePrivate && row.private) return false;
    if (visibilityFilters.hideForks && row.fork) return false;
    return selected.has(rowKey(row)) && !row.already_configured;
  });
}
