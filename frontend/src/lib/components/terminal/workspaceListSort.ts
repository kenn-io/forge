export type WorkspaceListSort = "repo" | "created" | "activity";

export const workspaceListSortOptions: {
  value: WorkspaceListSort;
  label: string;
}[] = [
  { value: "repo", label: "Org / repo" },
  { value: "created", label: "Created" },
  { value: "activity", label: "Activity" },
];

export const defaultWorkspaceListSort: WorkspaceListSort = "repo";

const storageKey = "middleman:workspaceListSort";

const validSorts = new Set<WorkspaceListSort>(workspaceListSortOptions.map((option) => option.value));

function getStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

export function loadWorkspaceListSort(): WorkspaceListSort {
  const storage = getStorage();
  if (!storage) return defaultWorkspaceListSort;

  try {
    const value = storage.getItem(storageKey) as WorkspaceListSort | null;
    return value && validSorts.has(value) ? value : defaultWorkspaceListSort;
  } catch {
    return defaultWorkspaceListSort;
  }
}

export function saveWorkspaceListSort(sort: WorkspaceListSort): void {
  const storage = getStorage();
  if (!storage) return;

  try {
    storage.setItem(storageKey, sort);
  } catch {
    // Storage blocked - the sort still applies for the current page instance.
  }
}
