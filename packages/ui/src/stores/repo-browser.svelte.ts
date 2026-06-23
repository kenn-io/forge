import type { RepoBrowserBlob, RepoBrowserCommit, RepoBrowserRef, RepoBrowserTreeEntry } from "../api/types.js";
import { providerRepoPath, providerRouteParams, type ProviderRouteRef } from "../api/provider-routes.js";
import type { MiddlemanClient } from "../types.js";
import type { components } from "../api/generated/schema.js";
import type { DiffFileCategoryCounts, DiffFileCategoryFilter } from "../utils/diff-categories.js";
import {
  buildSourceBrowserFileEntries,
  countSourceBrowserFileEntriesByCategory,
  filterSourceBrowserFileEntriesByCategory,
  type SourceBrowserFileEntry,
} from "../utils/source-browser-files.js";

export type RepoBrowserViewMode = "source" | "preview";

export interface RepoBrowserStoreOptions {
  client?: MiddlemanClient;
}

type Problem = { detail?: string; title?: string };
type RepoBrowserRefsResponse = components["schemas"]["RepoBrowserRefsResponse"];
type RepoBrowserTreeResponse = components["schemas"]["RepoBrowserTreeResponse"];
type RepoBrowserBlobResponse = components["schemas"]["RepoBrowserBlobResponse"];
type RepoBrowserHistoryResponse = components["schemas"]["RepoBrowserHistoryResponse"];
type RepoBrowserLastChangedResponse = components["schemas"]["RepoBrowserLastChangedResponse"];
type RepoBrowserCommitResponse = components["schemas"]["RepoBrowserCommitResponse"];

const viewModeStorageKey = "repo-browser-view-mode";
const validViewModes: RepoBrowserViewMode[] = ["source", "preview"];

function safeGetItem(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function safeSetItem(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* ignore */
  }
}

function loadViewMode(): RepoBrowserViewMode {
  const raw = safeGetItem(viewModeStorageKey);
  return validViewModes.includes(raw as RepoBrowserViewMode) ? (raw as RepoBrowserViewMode) : "source";
}

function apiErrorMessage(error: Problem | undefined, fallback: string): string {
  return error?.detail ?? error?.title ?? fallback;
}

function defaultRepoBrowserClient(): MiddlemanClient {
  throw new Error("repo browser store requires a client");
}

export function createRepoBrowserStore(opts: RepoBrowserStoreOptions = {}) {
  const client = opts.client ?? defaultRepoBrowserClient();

  let repo = $state<ProviderRouteRef | null>(null);
  let refs = $state<RepoBrowserRef[]>([]);
  let defaultRef = $state<RepoBrowserRef | null>(null);
  let selectedRef = $state<RepoBrowserRef | null>(null);
  let tree = $state<RepoBrowserTreeEntry[]>([]);
  let treeTruncated = $state(false);
  let lastChanged = $state<Record<string, RepoBrowserCommit>>({});
  let selectedPath = $state<string | null>(null);
  let blob = $state<RepoBrowserBlob | null>(null);
  let fileHistory = $state<RepoBrowserCommit[]>([]);
  let selectedCommit = $state<RepoBrowserCommit | null>(null);
  let fileCategoryFilter = $state<DiffFileCategoryFilter>("all");
  let viewMode = $state<RepoBrowserViewMode>(loadViewMode());
  let loading = $state(false);
  let blobLoading = $state(false);
  let error = $state<string | null>(null);

  const fileEntries = $derived(buildSourceBrowserFileEntries(tree, lastChanged));
  const visibleFileEntries = $derived(filterSourceBrowserFileEntriesByCategory(fileEntries, fileCategoryFilter));
  const fileCategoryCounts = $derived(countSourceBrowserFileEntriesByCategory(fileEntries));

  function queryFor(ref: ProviderRouteRef, selected: RepoBrowserRef | null = selectedRef) {
    return {
      repo_path: ref.repoPath,
      ...(selected && {
        ref_type: selected.type,
        ref_name: selected.name,
        ref_sha: selected.sha,
      }),
    };
  }

  async function loadRepo(
    nextRepo: ProviderRouteRef,
    initial?: { ref?: RepoBrowserRef; path?: string | null },
  ): Promise<void> {
    repo = nextRepo;
    loading = true;
    error = null;
    try {
      const {
        data,
        error: apiError,
        response,
      } = await client.GET(providerRepoPath(nextRepo, "/browser/refs"), {
        params: {
          path: providerRouteParams(nextRepo),
          query: { repo_path: nextRepo.repoPath },
        },
      });
      if (!data) throw new Error(apiErrorMessage(apiError, `HTTP ${response.status}`));
      applyRefs(data as RepoBrowserRefsResponse, initial?.ref);
      await loadTree(initial?.path ?? null);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      clearRepoData();
    } finally {
      loading = false;
    }
  }

  function applyRefs(response: RepoBrowserRefsResponse, requestedRef?: RepoBrowserRef | undefined): void {
    refs = response.refs ?? [];
    defaultRef = response.default_ref;
    selectedRef = chooseSelectedRef(requestedRef);
  }

  function chooseSelectedRef(requestedRef?: RepoBrowserRef | undefined): RepoBrowserRef | null {
    if (requestedRef) return requestedRef;
    return defaultRef ?? refs[0] ?? null;
  }

  async function loadTree(requestedPath: string | null = null): Promise<void> {
    const ref = repo;
    if (!ref || !selectedRef) return;
    const {
      data,
      error: apiError,
      response,
    } = await client.GET(providerRepoPath(ref, "/browser/tree"), {
      params: {
        path: providerRouteParams(ref),
        query: queryFor(ref),
      },
    });
    if (!data) throw new Error(apiErrorMessage(apiError, `HTTP ${response.status}`));
    const payload = data as RepoBrowserTreeResponse;
    tree = payload.entries ?? [];
    treeTruncated = payload.truncated;
    await loadLastChanged();
    const firstPath =
      requestedPath && tree.some((entry) => entry.path === requestedPath) ? requestedPath : (tree[0]?.path ?? null);
    if (firstPath) {
      await selectPath(firstPath);
    } else {
      selectedPath = null;
      blob = null;
      fileHistory = [];
    }
  }

  async function loadLastChanged(): Promise<void> {
    const ref = repo;
    if (!ref || !selectedRef) return;
    const paths = tree.slice(0, 250).map((entry) => entry.path);
    if (paths.length === 0) {
      lastChanged = {};
      return;
    }
    const {
      data,
      error: apiError,
      response,
    } = await client.GET(providerRepoPath(ref, "/browser/last-changed"), {
      params: {
        path: providerRouteParams(ref),
        query: {
          ...queryFor(ref),
          path: paths,
        },
      },
    });
    if (!data) throw new Error(apiErrorMessage(apiError, `HTTP ${response.status}`));
    lastChanged = (data as RepoBrowserLastChangedResponse).commits ?? {};
  }

  async function selectRef(ref: RepoBrowserRef): Promise<void> {
    selectedRef = ref;
    await loadTree(null);
  }

  async function selectPath(path: string): Promise<void> {
    const ref = repo;
    if (!ref || !selectedRef) return;
    selectedPath = path;
    blobLoading = true;
    error = null;
    try {
      const [{ data: blobData, error: blobError, response: blobResponse }, historyResponse] = await Promise.all([
        client.GET(providerRepoPath(ref, "/browser/blob"), {
          params: {
            path: providerRouteParams(ref),
            query: {
              ...queryFor(ref),
              path,
            },
          },
        }),
        client.GET(providerRepoPath(ref, "/browser/history"), {
          params: {
            path: providerRouteParams(ref),
            query: {
              ...queryFor(ref),
              path,
            },
          },
        }),
      ]);
      if (!blobData) throw new Error(apiErrorMessage(blobError, `HTTP ${blobResponse.status}`));
      if (!historyResponse.data) {
        throw new Error(apiErrorMessage(historyResponse.error, `HTTP ${historyResponse.response.status}`));
      }
      blob = (blobData as RepoBrowserBlobResponse).blob;
      fileHistory = (historyResponse.data as RepoBrowserHistoryResponse).commits ?? [];
      selectedCommit = fileHistory[0] ?? null;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      blob = null;
      fileHistory = [];
      selectedCommit = null;
    } finally {
      blobLoading = false;
    }
  }

  async function selectCommit(sha: string): Promise<void> {
    const ref = repo;
    if (!ref || !selectedRef || !selectedPath) return;
    const {
      data,
      error: apiError,
      response,
    } = await client.GET(providerRepoPath(ref, "/browser/commit"), {
      params: {
        path: providerRouteParams(ref),
        query: {
          ...queryFor(ref),
          path: selectedPath,
          sha,
        },
      },
    });
    if (!data) throw new Error(apiErrorMessage(apiError, `HTTP ${response.status}`));
    selectedCommit = (data as RepoBrowserCommitResponse).commit;
  }

  function clearRepoData(): void {
    refs = [];
    defaultRef = null;
    selectedRef = null;
    tree = [];
    treeTruncated = false;
    lastChanged = {};
    selectedPath = null;
    blob = null;
    fileHistory = [];
    selectedCommit = null;
  }

  function setFileCategoryFilter(filter: DiffFileCategoryFilter): void {
    fileCategoryFilter = filter;
  }

  function setViewMode(mode: RepoBrowserViewMode): void {
    viewMode = mode;
    safeSetItem(viewModeStorageKey, mode);
  }

  return {
    getRepo: () => repo,
    getRefs: () => refs,
    getDefaultRef: () => defaultRef,
    getSelectedRef: () => selectedRef,
    getTree: () => tree,
    isTreeTruncated: () => treeTruncated,
    getLastChanged: () => lastChanged,
    getSelectedPath: () => selectedPath,
    getBlob: () => blob,
    getFileHistory: () => fileHistory,
    getSelectedCommit: () => selectedCommit,
    getFileEntries: (): SourceBrowserFileEntry[] => fileEntries,
    getVisibleFileEntries: (): SourceBrowserFileEntry[] => visibleFileEntries,
    getFileCategoryFilter: () => fileCategoryFilter,
    getFileCategoryCounts: (): DiffFileCategoryCounts => fileCategoryCounts,
    getViewMode: () => viewMode,
    isLoading: () => loading,
    isBlobLoading: () => blobLoading,
    getError: () => error,
    loadRepo,
    selectRef,
    selectPath,
    selectCommit,
    setFileCategoryFilter,
    setViewMode,
  };
}

export type RepoBrowserStore = ReturnType<typeof createRepoBrowserStore>;
