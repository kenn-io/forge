import type {
  DiffResult,
  FilePreview,
  FilesResult,
  CommitInfo,
} from "../api/types.js";
import { createAPIClient } from "../api/generated/client.js";
import {
  providerItemPath,
  providerRepoPath,
  providerRouteParams,
  type ProviderRouteRef,
} from "../api/provider-routes.js";
import type { components } from "../api/generated/schema.js";
import type { MiddlemanClient } from "../types.js";
import {
  countDiffFilesByCategory,
  filterDiffFilesByCategory,
  type DiffFileCategoryCounts,
  type DiffFileCategoryFilter,
} from "../utils/diff-categories.js";

export type DiffScope =
  | { kind: "head" }
  | { kind: "commit"; sha: string }
  | { kind: "range"; fromSha: string; toSha: string };

export type WorkspaceDiffBase = "head" | "pushed" | "merge-target";

export type DiffScrollTarget = {
  path: string;
  line?: number | undefined;
  side?: "left" | "right" | undefined;
};

export interface DiffStoreOptions {
  client?: MiddlemanClient;
  getBasePath?: () => string;
}

function apiErrorMessage(
  error: { detail?: string; title?: string } | undefined,
  fallback: string,
): string {
  return error?.detail ?? error?.title ?? fallback;
}

type DiffResponse = components["schemas"]["DiffResponse"];
type FilesResponse = components["schemas"]["FilesResponse"];

function normalizeDiffResult(data: DiffResponse): DiffResult {
  return {
    ...data,
    files: data.files ?? [],
  } as DiffResult;
}

function normalizeFilesResult(data: FilesResponse): FilesResult {
  return {
    ...data,
    files: data.files ?? [],
  } as FilesResult;
}

function apiBaseURL(basePath: string): string {
  const path = `${basePath.replace(/\/$/, "")}/api/v1`;
  if (typeof window !== "undefined") {
    return new URL(path, window.location.origin).toString();
  }
  return `http://localhost${path}`;
}

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

const VALID_TAB_WIDTHS = [1, 2, 4, 8];

function loadTabWidth(): number {
  const raw = parseInt(
    safeGetItem("diff-tab-width") ?? "4",
    10,
  );
  return VALID_TAB_WIDTHS.includes(raw) ? raw : 4;
}

function loadCollapsedFiles(): Record<string, string[]> {
  try {
    const raw = safeGetItem("diff-collapsed-files");
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (
      typeof parsed !== "object" ||
      parsed === null ||
      Array.isArray(parsed)
    )
      return {};
    const result: Record<string, string[]> = {};
    for (const [key, value] of Object.entries(
      parsed as Record<string, unknown>,
    )) {
      if (
        Array.isArray(value) &&
        value.every((v) => typeof v === "string")
      ) {
        result[key] = value as string[];
      }
    }
    return result;
  } catch {
    return {};
  }
}

function saveCollapsedFiles(
  cf: Record<string, string[]>,
): void {
  safeSetItem("diff-collapsed-files", JSON.stringify(cf));
}

export function createDiffStore(opts?: DiffStoreOptions) {
  const getBasePath = opts?.getBasePath ?? (() => "/");
  const apiClient =
    opts?.client ??
    createAPIClient(apiBaseURL(getBasePath()), {
      fetch: globalThis.fetch.bind(globalThis),
    });

  let diff = $state<DiffResult | null>(null);
  let loading = $state(false);
  let storeError = $state<string | null>(null);
  let abortController: AbortController | null = null;

  let fileList = $state<FilesResult | null>(null);
  let fileListLoading = $state(false);
  let fileListAbortController: AbortController | null = null;

  let tabWidth = $state(loadTabWidth());
  let wordWrap = $state(
    safeGetItem("diff-word-wrap") === "true",
  );
  let richPreview = $state(
    safeGetItem("diff-rich-preview") === "true",
  );
  let hideWhitespace = $state(
    safeGetItem("diff-hide-whitespace") === "true",
  );
  let collapsedFiles = $state<Record<string, string[]>>(
    loadCollapsedFiles(),
  );
  let activeFile = $state<string | null>(null);
  let scrollTarget = $state<DiffScrollTarget | null>(null);
  let scrolling = $state(false);
  let fileCategoryFilter = $state<DiffFileCategoryFilter>("all");
  let commits = $state<CommitInfo[] | null>(null);
  let commitsLoading = $state(false);
  let commitsError = $state<string | null>(null);
  let scope = $state<DiffScope>({ kind: "head" });
  let filePreviewGeneration = $state(0);
  const filePreviewCache = new Map<string, Promise<FilePreview>>();

  let currentOwner = $state("");
  let currentName = $state("");
  let currentNumber = $state(0);
  let currentWorkspaceID = $state("");
  let currentWorkspaceBase = $state<WorkspaceDiffBase>("head");
  let currentWorkspaceStacked = $state(false);
  let currentCommitSHA = $state("");
  let currentProvider = $state("");
  let currentPlatformHost = $state<string | undefined>(undefined);
  let currentRepoPath = $state("");

  function getCurrentPR(): (ProviderRouteRef & { number: number }) | null {
    if (!currentOwner) return null;
    return {
      provider: currentProvider,
      platformHost: currentPlatformHost,
      owner: currentOwner,
      name: currentName,
      repoPath: currentRepoPath,
      number: currentNumber,
    };
  }

  function currentRouteRef(): ProviderRouteRef {
    return {
      provider: currentProvider,
      platformHost: currentPlatformHost,
      owner: currentOwner,
      name: currentName,
      repoPath: currentRepoPath,
    };
  }

  // --- reads ---

  function getDiff(): DiffResult | null {
    return diff;
  }
  function isDiffLoading(): boolean {
    return loading;
  }
  function getDiffError(): string | null {
    return storeError;
  }
  function getFileList(): FilesResult | null {
    if (currentWorkspaceID && fileList) {
      return { stale: fileList.stale, files: fileList.files ?? [] };
    }
    // Prefer diff.files once available — it respects hideWhitespace
    // and is authoritative. The lightweight /files response is a fast
    // preview used only until the full diff arrives.
    if (diff) return { stale: diff.stale, files: diff.files ?? [] };
    if (fileList) return { stale: fileList.stale, files: fileList.files ?? [] };
    return null;
  }
  function getVisibleFileList(): FilesResult | null {
    const list = getFileList();
    if (!list) return null;
    return {
      stale: list.stale,
      files: filterDiffFilesByCategory(list.files, fileCategoryFilter),
    };
  }
  function getVisibleDiffFiles(): DiffResult["files"] {
    if (!diff) return [];
    return filterDiffFilesByCategory(diff.files ?? [], fileCategoryFilter);
  }
  function getFileCategoryCounts(): DiffFileCategoryCounts {
    return countDiffFilesByCategory(getFileList()?.files ?? []);
  }
  function isFileListLoading(): boolean {
    // Show loading until we have *some* file data. When /files fails
    // but /diff is still in flight, keep showing loading state.
    return !diff && (fileListLoading || loading);
  }
  function getTabWidth(): number {
    return tabWidth;
  }
  function getWordWrap(): boolean {
    return wordWrap;
  }
  function getRichPreview(): boolean {
    return richPreview;
  }
  function getFilePreviewGeneration(): number {
    return filePreviewGeneration;
  }
  function getHideWhitespace(): boolean {
    return hideWhitespace;
  }
  function getFileCategoryFilter(): DiffFileCategoryFilter {
    return fileCategoryFilter;
  }
  function getActiveFile(): string | null {
    return activeFile;
  }
  function isScrolling(): boolean {
    return scrolling;
  }

  function isFileCollapsed(
    owner: string,
    name: string,
    number: number,
    filePath: string,
  ): boolean {
    const key = `${owner}/${name}#${number}`;
    return (collapsedFiles[key] ?? []).includes(filePath);
  }

  // --- writes ---

  function setActiveFile(path: string | null): void {
    activeFile = path;
  }

  function setFileCategoryFilter(nextFilter: DiffFileCategoryFilter): void {
    fileCategoryFilter = nextFilter;
    const visibleFiles = getVisibleFileList()?.files ?? getVisibleDiffFiles();
    setActiveIfNeeded(visibleFiles);
  }

  function clearScrolling(): void {
    scrolling = false;
  }

  function requestScrollToFile(path: string): void {
    activeFile = path;
    scrolling = true;
    scrollTarget = { path };
  }

  function requestScrollToLine(
    path: string,
    line: number,
    side: "left" | "right" = "right",
  ): void {
    activeFile = path;
    scrolling = true;
    scrollTarget = { path, line, side };
  }

  function getScrollTarget(): DiffScrollTarget | null {
    return scrollTarget;
  }

  function consumeScrollTarget(): void {
    scrollTarget = null;
  }

  function setTabWidth(w: number): void {
    tabWidth = w;
    safeSetItem("diff-tab-width", String(w));
  }

  function setWordWrap(v: boolean): void {
    wordWrap = v;
    safeSetItem("diff-word-wrap", String(v));
  }

  function setRichPreview(v: boolean): void {
    richPreview = v;
    safeSetItem("diff-rich-preview", String(v));
  }

  function setHideWhitespace(v: boolean): void {
    hideWhitespace = v;
    safeSetItem("diff-hide-whitespace", String(v));
    if (currentOwner && currentName && currentNumber) {
      void reloadDiffOnly();
    } else if (currentOwner && currentName && currentCommitSHA) {
      void reloadCommitDiffOnly();
    } else if (currentWorkspaceID) {
      void reloadWorkspaceDiffOnly();
    }
  }

  async function reloadDiffOnly(): Promise<void> {
    abortController?.abort();
    // Abort any in-flight /files request so a late response from a
    // prior loadDiff() cannot repopulate fileList after we clear it.
    fileListAbortController?.abort();
    fileListAbortController = null;
    fileListLoading = false;
    const ac = new AbortController();
    abortController = ac;
    fileList = null;
    clearFilePreviewCache();

    loading = true;
    storeError = null;
    const ref = currentRouteRef();
    try {
      const { data, error, response } = await apiClient.GET(
        providerItemPath("pulls", ref, "/diff"),
        {
          params: {
            path: {
              ...providerRouteParams(ref),
              number: currentNumber,
            },
            query: diffQuery(),
          },
          signal: ac.signal,
        },
      );
      if (abortController !== ac) return;
      if (!data) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      const result = normalizeDiffResult(data);
      diff = result;
      setActiveIfNeeded(getVisibleDiffFiles());
    } catch (err) {
      if (ac.signal.aborted) return;
      if (abortController !== ac) return;
      storeError =
        err instanceof Error ? err.message : String(err);
      diff = null;
    } finally {
      if (!ac.signal.aborted && abortController === ac) {
        loading = false;
      }
    }
  }

  async function reloadWorkspaceDiffOnly(): Promise<void> {
    await loadWorkspaceDiff(
      currentWorkspaceID,
      currentWorkspaceBase,
      currentWorkspaceStacked,
    );
  }

  async function reloadCommitDiffOnly(): Promise<void> {
    await loadCommitDiff(currentRouteRef(), currentCommitSHA);
  }

  function toggleFileCollapsed(
    owner: string,
    name: string,
    number: number,
    filePath: string,
  ): void {
    const key = `${owner}/${name}#${number}`;
    const current = collapsedFiles[key] ?? [];
    if (current.includes(filePath)) {
      collapsedFiles = {
        ...collapsedFiles,
        [key]: current.filter((f) => f !== filePath),
      };
    } else {
      collapsedFiles = {
        ...collapsedFiles,
        [key]: [...current, filePath],
      };
    }
    saveCollapsedFiles(collapsedFiles);
  }

  function diffQuery(): {
    whitespace?: string;
    commit?: string;
    from?: string;
    to?: string;
  } {
    return {
      ...(hideWhitespace && { whitespace: "hide" }),
      ...(scope.kind === "commit" && { commit: scope.sha }),
      ...(scope.kind === "range" && {
        from: scope.fromSha,
        to: scope.toSha,
      }),
    };
  }

  function scopeCacheKey(): string {
    if (scope.kind === "head") return "head";
    if (scope.kind === "commit") return `commit:${scope.sha}`;
    return `range:${scope.fromSha}:${scope.toSha}`;
  }

  function clearFilePreviewCache(): void {
    filePreviewCache.clear();
    filePreviewGeneration += 1;
  }

  async function loadFilePreview(
    owner: string,
    name: string,
    number: number,
    path: string,
  ): Promise<FilePreview> {
    const ref = currentRouteRef();
    const key = `${ref.provider}:${ref.platformHost ?? ""}:${ref.repoPath}#${number}:${scopeCacheKey()}:${path}`;
    const cached = filePreviewCache.get(key);
    if (cached) return cached;

    const request = (async () => {
      const { data, error, response } = await apiClient.GET(
        providerItemPath("pulls", ref, "/file-preview"),
        {
          params: {
            path: { ...providerRouteParams(ref), number },
            query: {
              path,
              ...(scope.kind === "commit" && { commit: scope.sha }),
              ...(scope.kind === "range" && {
                from: scope.fromSha,
                to: scope.toSha,
              }),
            },
          },
        },
      );
      if (!data) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      return data as FilePreview;
    })();

    filePreviewCache.set(key, request);
    try {
      return await request;
    } catch (err) {
      filePreviewCache.delete(key);
      throw err;
    }
  }

  function workspaceDiffQuery(base: WorkspaceDiffBase): {
    base: WorkspaceDiffBase;
    whitespace?: string;
    commit?: string;
    from?: string;
    to?: string;
  } {
    return {
      base,
      ...diffQuery(),
    };
  }

  function setActiveIfNeeded(
    files: { path: string }[] | undefined,
  ): void {
    if (
      !files?.some((f) => f.path === activeFile)
    ) {
      activeFile = files?.[0]?.path ?? null;
    }
  }

  function resetDiffScopeState(): void {
    scope = { kind: "head" };
    fileCategoryFilter = "all";
    commits = null;
    commitsLoading = false;
    commitsError = null;
  }

  function startDiffLoad(): {
    diffAc: AbortController;
    filesAc: AbortController;
  } {
    abortController?.abort();
    fileListAbortController?.abort();
    const diffAc = new AbortController();
    const filesAc = new AbortController();
    abortController = diffAc;
    fileListAbortController = filesAc;

    diff = null;
    fileList = null;
    loading = true;
    fileListLoading = true;
    storeError = null;

    return { diffAc, filesAc };
  }

  function filesLoadIsCurrent(filesAc: AbortController): boolean {
    return fileListAbortController === filesAc;
  }

  function diffLoadIsCurrent(diffAc: AbortController): boolean {
    return abortController === diffAc;
  }

  function finishFilesLoad(filesAc: AbortController): void {
    if (!filesAc.signal.aborted && filesLoadIsCurrent(filesAc)) {
      fileListLoading = false;
    }
  }

  function finishDiffLoad(diffAc: AbortController): void {
    if (!diffAc.signal.aborted && diffLoadIsCurrent(diffAc)) {
      loading = false;
    }
  }

  function applyFilesResult(data: FilesResponse): void {
    fileList = normalizeFilesResult(data);
    setActiveIfNeeded(getVisibleFileList()?.files);
  }

  function applyDiffResult(data: DiffResponse): void {
    diff = normalizeDiffResult(data);
    setActiveIfNeeded(getVisibleDiffFiles());
  }

  function failDiffLoad(
    err: unknown,
    diffAc: AbortController,
    filesAc: AbortController,
  ): void {
    if (diffAc.signal.aborted || !diffLoadIsCurrent(diffAc)) return;

    storeError = err instanceof Error ? err.message : String(err);
    diff = null;
    fileList = null;
    fileListAbortController = null;
    filesAc.abort();
    fileListLoading = false;
    finishDiffLoad(diffAc);
  }

  async function loadDiff(
    owner: string,
    name: string,
    number: number,
    identity: ProviderRouteRef,
  ): Promise<void> {
    const prChanged =
      owner !== currentOwner ||
      name !== currentName ||
      number !== currentNumber;
    currentOwner = owner;
    currentName = name;
    currentNumber = number;
    clearFilePreviewCache();
    currentWorkspaceID = "";
    currentCommitSHA = "";
    currentProvider = identity.provider;
    currentPlatformHost = identity.platformHost;
    currentRepoPath = identity.repoPath;
    if (prChanged) {
      resetDiffScopeState();
    }

    const { diffAc, filesAc } = startDiffLoad();
    const ref = currentRouteRef();

    const filesPromise = (async () => {
      try {
        const { data } = await apiClient.GET(
          providerItemPath("pulls", ref, "/files"),
          {
            params: { path: { ...providerRouteParams(ref), number } },
            signal: filesAc.signal,
          },
        );
        if (!filesLoadIsCurrent(filesAc)) return;
        if (!data) return;
        applyFilesResult(data);
      } catch {
        if (filesAc.signal.aborted) return;
        if (!filesLoadIsCurrent(filesAc)) return;
        fileList = null;
      } finally {
        finishFilesLoad(filesAc);
      }
    })();

    const diffPromise = (async () => {
      try {
        const { data, error, response } = await apiClient.GET(
          providerItemPath("pulls", ref, "/diff"),
          {
            params: {
              path: { ...providerRouteParams(ref), number },
              query: diffQuery(),
            },
            signal: diffAc.signal,
          },
        );
        if (!diffLoadIsCurrent(diffAc)) return;
        if (!data) {
          throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
        }
        applyDiffResult(data);
      } catch (_err) {
        failDiffLoad(_err, diffAc, filesAc);
      } finally {
        finishDiffLoad(diffAc);
      }
    })();

    await Promise.allSettled([filesPromise, diffPromise]);
  }

  async function loadWorkspaceDiff(
    workspaceID: string,
    base: WorkspaceDiffBase,
    stacked = false,
  ): Promise<void> {
    const workspaceScopeChanged =
      workspaceID !== currentWorkspaceID ||
      base !== currentWorkspaceBase;
    currentWorkspaceID = workspaceID;
    currentWorkspaceBase = base;
    currentWorkspaceStacked = stacked;
    currentOwner = "";
    currentName = "";
    currentNumber = 0;
    currentCommitSHA = "";
    if (workspaceScopeChanged) {
      resetDiffScopeState();
    }

    const { diffAc, filesAc } = startDiffLoad();

    try {
      const { data, error, response } = await apiClient.GET(
        "/workspaces/{id}/files",
        {
          params: {
            path: { id: workspaceID },
            query: workspaceDiffQuery(base),
          },
          signal: filesAc.signal,
        },
      );
      if (!filesLoadIsCurrent(filesAc)) return;
      if (!data) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      applyFilesResult(data);
    } catch (_err) {
      if (filesAc.signal.aborted || !filesLoadIsCurrent(filesAc)) return;
      failDiffLoad(_err, diffAc, filesAc);
      return;
    } finally {
      finishFilesLoad(filesAc);
    }

    try {
      const { data, error, response } = await apiClient.GET(
        "/workspaces/{id}/diff",
        {
          params: {
            path: { id: workspaceID },
            query: workspaceDiffQuery(base),
          },
          signal: diffAc.signal,
        },
      );
      if (!diffLoadIsCurrent(diffAc)) return;
      if (!data) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      applyDiffResult(data);
    } catch (_err) {
      failDiffLoad(_err, diffAc, filesAc);
    } finally {
      finishDiffLoad(diffAc);
    }
  }

  async function loadCommitDiff(
    identity: ProviderRouteRef,
    sha: string,
  ): Promise<void> {
    const commitChanged =
      identity.owner !== currentOwner ||
      identity.name !== currentName ||
      sha !== currentCommitSHA;
    currentOwner = identity.owner;
    currentName = identity.name;
    currentNumber = 0;
    currentWorkspaceID = "";
    currentCommitSHA = sha;
    currentProvider = identity.provider;
    currentPlatformHost = identity.platformHost;
    currentRepoPath = identity.repoPath;
    if (commitChanged) {
      resetDiffScopeState();
    }
    clearFilePreviewCache();

    abortController?.abort();
    fileListAbortController?.abort();
    fileListAbortController = null;
    fileListLoading = false;
    const diffAc = new AbortController();
    abortController = diffAc;
    diff = null;
    fileList = null;
    loading = true;
    storeError = null;

    const ref = currentRouteRef();
    try {
      const { data, error, response } = await apiClient.GET(
        providerRepoPath(ref, "/commits/{sha}/diff"),
        {
          params: {
            path: {
              ...providerRouteParams(ref),
              sha,
            },
            query: {
              ...(hideWhitespace && { whitespace: "hide" }),
            },
          },
          signal: diffAc.signal,
        },
      );
      if (!diffLoadIsCurrent(diffAc)) return;
      if (!data) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      applyDiffResult(data);
    } catch (err) {
      if (diffAc.signal.aborted || !diffLoadIsCurrent(diffAc)) return;
      storeError = err instanceof Error ? err.message : String(err);
      diff = null;
      fileList = null;
    } finally {
      finishDiffLoad(diffAc);
    }
  }

  function clearDiff(): void {
    abortController?.abort();
    abortController = null;
    fileListAbortController?.abort();
    fileListAbortController = null;
    diff = null;
    fileList = null;
    storeError = null;
    loading = false;
    fileListLoading = false;
    activeFile = null;
    scrollTarget = null;
    scrolling = false;
    fileCategoryFilter = "all";
    commits = null;
    commitsLoading = false;
    commitsError = null;
    scope = { kind: "head" };
    clearFilePreviewCache();
    currentOwner = "";
    currentName = "";
    currentNumber = 0;
    currentWorkspaceID = "";
    currentWorkspaceBase = "head";
    currentWorkspaceStacked = false;
    currentCommitSHA = "";
    currentProvider = "";
    currentPlatformHost = undefined;
    currentRepoPath = "";
  }

  async function loadCommits(): Promise<void> {
    if (commits || commitsLoading) return;
    if (!currentWorkspaceID && (!currentOwner || !currentName || !currentNumber)) {
      return;
    }

    commitsLoading = true;
    commitsError = null;
    const owner = currentOwner;
    const name = currentName;
    const number = currentNumber;
    const workspaceID = currentWorkspaceID;
    const ref = currentRouteRef();
    try {
      const { data, error, response } = workspaceID
        ? await apiClient.GET(
          "/workspaces/{id}/commits",
          {
            params: { path: { id: workspaceID } },
          },
        )
        : await apiClient.GET(
          providerItemPath("pulls", ref, "/commits"),
          {
            params: { path: { ...providerRouteParams(ref), number } },
          },
        );
      if (
        currentWorkspaceID !== workspaceID ||
        currentOwner !== owner ||
        currentName !== name ||
        currentNumber !== number
      ) return;
      if (!data) {
        throw new Error(apiErrorMessage(error, `HTTP ${response.status}`));
      }
      if (
        currentWorkspaceID !== workspaceID ||
        currentOwner !== owner ||
        currentName !== name ||
        currentNumber !== number
      ) return;
      commits = data.commits ?? [];
    } catch (err) {
      if (
        currentWorkspaceID !== workspaceID ||
        currentOwner !== owner ||
        currentName !== name ||
        currentNumber !== number
      ) return;
      commitsError = err instanceof Error ? err.message : String(err);
    } finally {
      if (
        currentWorkspaceID === workspaceID &&
        currentOwner === owner &&
        currentName === name &&
        currentNumber === number
      ) {
        commitsLoading = false;
      }
    }
  }

  function getScope(): DiffScope {
    return scope;
  }

  function getCommits(): CommitInfo[] | null {
    return commits;
  }

  function isCommitsLoading(): boolean {
    return commitsLoading;
  }

  function getCommitsError(): string | null {
    return commitsError;
  }

  function selectCommit(sha: string): void {
    scope = { kind: "commit", sha };
    clearFilePreviewCache();
    if (currentOwner && currentName && currentNumber) {
      void loadDiff(currentOwner, currentName, currentNumber, currentRouteRef());
    } else if (currentWorkspaceID) {
      void loadWorkspaceDiff(
        currentWorkspaceID,
        currentWorkspaceBase,
        currentWorkspaceStacked,
      );
    }
  }

  function selectRange(fromSha: string, toSha: string): void {
    if (!commits) return;
    const fromIdx = commits.findIndex((c) => c.sha === fromSha);
    const toIdx = commits.findIndex((c) => c.sha === toSha);
    if (fromIdx === -1 || toIdx === -1) return;
    const [older, newer] = fromIdx > toIdx ? [fromSha, toSha] : [toSha, fromSha];
    scope = { kind: "range", fromSha: older, toSha: newer };
    clearFilePreviewCache();
    if (currentOwner && currentName && currentNumber) {
      void loadDiff(currentOwner, currentName, currentNumber, currentRouteRef());
    } else if (currentWorkspaceID) {
      void loadWorkspaceDiff(
        currentWorkspaceID,
        currentWorkspaceBase,
        currentWorkspaceStacked,
      );
    }
  }

  function resetToHead(): void {
    scope = { kind: "head" };
    clearFilePreviewCache();
    if (currentOwner && currentName && currentNumber) {
      void loadDiff(currentOwner, currentName, currentNumber, currentRouteRef());
    } else if (currentWorkspaceID) {
      void loadWorkspaceDiff(
        currentWorkspaceID,
        currentWorkspaceBase,
        currentWorkspaceStacked,
      );
    }
  }

  function stepPrev(): void {
    if (!commits) {
      void loadCommits();
      return;
    }
    if (commits.length === 0) return;
    const s = scope;
    if (s.kind === "head") {
      selectCommit(commits[0]!.sha);
    } else if (s.kind === "commit") {
      const idx = commits.findIndex((c) => c.sha === s.sha);
      if (idx < commits.length - 1) selectCommit(commits[idx + 1]!.sha);
    } else {
      selectCommit(s.fromSha);
    }
  }

  function stepNext(): void {
    if (!commits) {
      void loadCommits();
      return;
    }
    if (commits.length === 0) return;
    const s = scope;
    if (s.kind === "head") {
      return;
    } else if (s.kind === "commit") {
      const idx = commits.findIndex((c) => c.sha === s.sha);
      if (idx > 0) {
        selectCommit(commits[idx - 1]!.sha);
      } else {
        resetToHead();
      }
    } else {
      selectCommit(s.toSha);
    }
  }

  return {
    getDiff,
    getCurrentPR,
    isDiffLoading,
    getDiffError,
    getFileList,
    getVisibleFileList,
    getVisibleDiffFiles,
    getFileCategoryCounts,
    isFileListLoading,
    getTabWidth,
    getWordWrap,
    getRichPreview,
    getFilePreviewGeneration,
    getHideWhitespace,
    getFileCategoryFilter,
    getActiveFile,
    setActiveFile,
    setFileCategoryFilter,
    isScrolling,
    clearScrolling,
    requestScrollToFile,
    requestScrollToLine,
    getScrollTarget,
    consumeScrollTarget,
    setTabWidth,
    setWordWrap,
    setRichPreview,
    setHideWhitespace,
    isFileCollapsed,
    toggleFileCollapsed,
    loadDiff,
    loadCommitDiff,
    loadFilePreview,
    loadWorkspaceDiff,
    clearDiff,
    getScope,
    getCommits,
    isCommitsLoading,
    getCommitsError,
    loadCommits,
    selectCommit,
    selectRange,
    resetToHead,
    stepPrev,
    stepNext,
  };
}

export type DiffStore = ReturnType<typeof createDiffStore>;
