export type WorkspaceListSort = "repo" | "created" | "activity" | "item-activity";

export interface WorkspaceListDisplayOptions {
  showOrgNames: boolean;
  showDiffStats: boolean;
}

export const workspaceListSortOptions: {
  value: WorkspaceListSort;
  label: string;
  description: string;
}[] = [
  {
    value: "repo",
    label: "Org / repo",
    description: "Group by repository, with newest workspaces first inside each repo.",
  },
  {
    value: "created",
    label: "Created",
    description: "Sort all workspaces by when the workspace was created.",
  },
  {
    value: "activity",
    label: "Activity",
    description: "Sort by latest terminal output, falling back to workspace creation.",
  },
  {
    value: "item-activity",
    label: "Item activity",
    description: "Sort by latest linked PR or issue activity, falling back to workspace creation.",
  },
];

export const defaultWorkspaceListSort: WorkspaceListSort = "repo";

export const defaultWorkspaceListDisplayOptions: WorkspaceListDisplayOptions = {
  showOrgNames: true,
  showDiffStats: true,
};

const sortStorageKey = "middleman:workspaceListSort";
const displayStorageKey = "middleman:workspaceListDisplayOptions";

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
    const value = storage.getItem(sortStorageKey) as WorkspaceListSort | null;
    return value && validSorts.has(value) ? value : defaultWorkspaceListSort;
  } catch {
    return defaultWorkspaceListSort;
  }
}

export function saveWorkspaceListSort(sort: WorkspaceListSort): void {
  const storage = getStorage();
  if (!storage) return;

  try {
    storage.setItem(sortStorageKey, sort);
  } catch {
    // Storage blocked - the sort still applies for the current page instance.
  }
}

export function loadWorkspaceListDisplayOptions(): WorkspaceListDisplayOptions {
  const storage = getStorage();
  if (!storage) return { ...defaultWorkspaceListDisplayOptions };

  try {
    const raw = storage.getItem(displayStorageKey);
    if (!raw) return { ...defaultWorkspaceListDisplayOptions };

    const value = JSON.parse(raw) as Partial<WorkspaceListDisplayOptions>;
    return {
      showOrgNames:
        typeof value.showOrgNames === "boolean" ? value.showOrgNames : defaultWorkspaceListDisplayOptions.showOrgNames,
      showDiffStats:
        typeof value.showDiffStats === "boolean"
          ? value.showDiffStats
          : defaultWorkspaceListDisplayOptions.showDiffStats,
    };
  } catch {
    return { ...defaultWorkspaceListDisplayOptions };
  }
}

export function saveWorkspaceListDisplayOptions(options: WorkspaceListDisplayOptions): void {
  const storage = getStorage();
  if (!storage) return;

  try {
    storage.setItem(displayStorageKey, JSON.stringify(options));
  } catch {
    // Storage blocked - the display options still apply for this page instance.
  }
}
