<script lang="ts">
  import { SearchInput, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import { untrack } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import {
    buildRepoBrowserRoute,
    createRepoBrowserStore,
    diffFileCategoryOptions,
    PierreFileTree,
    SplitResizeHandle,
    type DiffFileCategoryFilter,
    type FileTreeEntry,
    type MiddlemanClient,
    type RepoBrowserRouteRef,
    type RepoBrowserViewMode,
    type SourceBrowserFileEntry,
    type SplitResizeEvent,
  } from "@middleman/ui";
  import type { RepoBrowserCommit, RepoBrowserRef } from "@middleman/ui/api/types";
  import { providerDefaultHost } from "@middleman/ui/api/provider-routes";
  import DocMarkdownView from "../../components/docs/DocMarkdownView.svelte";
  import { RefreshIcon, ExternalLinkIcon, SpinnerIcon } from "../../icons";
  import {
    chooseRepoBrowserInitialPath,
    formatRepoBrowserCommitAge,
    formatRepoBrowserCommitDate,
    formatRepoBrowserFileSize,
    isRepoBrowserMarkdownPath,
  } from "./repoBrowserViewState.js";
  import PierreFileContents from "./PierreFileContents.svelte";
  import { apiBaseURL } from "../../api/runtime.js";
  import type { FolderIndex } from "../../api/docs/folderLinks";

  type RepoBrowserFeatureRoute = {
    page: "repo-browser";
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    owner: string;
    name: string;
    refType?: string | undefined;
    refName?: string | undefined;
    refSHA?: string | undefined;
    path?: string | undefined;
    mode?: RepoBrowserViewMode | undefined;
    anchor?: string | undefined;
  };

  interface Props {
    client: MiddlemanClient;
    route: RepoBrowserFeatureRoute;
    onRouteChange: (route: RepoBrowserRouteRef, options?: { replace?: boolean }) => void;
  }

  type RepoBrowserRouteUpdate = Partial<Pick<RepoBrowserFeatureRoute, "path" | "mode">> & {
    anchor?: string | null;
  };
  type RefPickerType = "branch" | "tag";

  const DEFAULT_FILES_WIDTH = 340;
  const DEFAULT_HISTORY_WIDTH = 320;
  const MIN_RAIL_WIDTH = 260;
  const MIN_VIEWER_WIDTH = 360;
  const RESIZE_HANDLE_WIDTH = 4;

  let { client, route, onRouteChange }: Props = $props();

  // svelte-ignore state_referenced_locally
  const store = createRepoBrowserStore({ client });

  let repoLoadKey = "";
  let repoLoadAliasKey = "";
  let routeLoadGeneration = 0;
  let pathSelectionGeneration = 0;
  let routeAnchorKey = "";
  let pathFilter = $state("");
  let selectedPathRevealKey = $state(0);
  let pendingMarkdownAnchor = $state(initialMarkdownAnchor());
  let refPickerType = $state<RefPickerType>("branch");
  let refPickerQuery = $state("");
  let refPickerError = $state("");
  let refPickerSelectionInFlight = $state(false);
  const refPickerRenderLimit = 100;
  let contentEl = $state<HTMLElement | null>(null);
  let sidebarEl = $state<HTMLElement | null>(null);
  let contentWidth = $state(0);
  let filesWidth = $state(DEFAULT_FILES_WIDTH);
  let filesResizeStartWidth = DEFAULT_FILES_WIDTH;
  let historyWidth = $state(DEFAULT_HISTORY_WIDTH);
  let historyResizeStartWidth = DEFAULT_HISTORY_WIDTH;
  let historyRailVisible = $state(true);

  const selectedPath = $derived(store.getSelectedPath());
  const selectedRef = $derived(store.getSelectedRef());
  const selectedBlob = $derived(store.getBlob());
  const selectedCommitDetail = $derived(store.getSelectedCommit());
  const selectedFile = $derived(findSelectedFile(store.getFileEntries(), selectedPath));
  const selectedIsMarkdown = $derived(isRepoBrowserMarkdownPath(selectedPath));
  const viewMode = $derived(store.getViewMode());
  const canPreview = $derived(selectedIsMarkdown && selectedBlob !== null && !selectedBlob.binary && !selectedBlob.too_large);
  const shownFiles = $derived.by(() => {
    const query = pathFilter.trim().toLowerCase();
    const files = store.getVisibleFileEntries();
    if (!query) return files;
    return files.filter((entry) => entry.path.toLowerCase().includes(query));
  });
  const treeEntries = $derived(shownFiles.map(toTreeEntry));
  const categoryCounts = $derived(store.getFileCategoryCounts());
  const visibleCategoryOptions = $derived(
    diffFileCategoryOptions.filter((option) => option.value === "all" || categoryCounts[option.value] > 0),
  );
  const markdownIndex = $derived(buildMarkdownIndex(store.getFileEntries()));
  const forgeHref = $derived(buildForgeHref(route, selectedRef, selectedPath));
  const branchRefs = $derived(store.getRefs().filter((ref) => refPickerRefType(ref) === "branch"));
  const tagRefs = $derived(store.getRefs().filter((ref) => refPickerRefType(ref) === "tag"));
  const refPickerFilteredRefs = $derived.by(() => {
    const query = refPickerQuery.trim().toLowerCase();
    const refs = refPickerType === "branch" ? branchRefs : tagRefs;
    if (!query) return refs;
    return refs.filter((ref) =>
      [ref.type, ref.name, ref.sha].some((part) => part.toLowerCase().includes(query)),
    );
  });
  const refPickerOptions = $derived<TypeaheadOption[]>(
    refPickerFilteredRefs.slice(0, refPickerRenderLimit).map((ref) => ({
      name: refKey(ref),
      label: ref.name || ref.sha.slice(0, 12),
      displayLabel: refOptionLabel(ref),
      meta: ref.sha.slice(0, 8),
    })),
  );
  const refPickerTruncated = $derived(refPickerFilteredRefs.length > refPickerRenderLimit);

  $effect(() => {
    if (refPickerSelectionInFlight || refPickerError) return;
    const type = selectedRef ? refPickerRefType(selectedRef) : null;
    if (type) {
      refPickerType = type;
    } else if (branchRefs.length === 0 && tagRefs.length > 0) {
      refPickerType = "tag";
    }
  });

  $effect(() => {
    if (!contentEl || !sidebarEl) return;

    updateSplitMeasurements();

    const observers: ResizeObserver[] = [];
    if (typeof ResizeObserver !== "undefined") {
      const observer = new ResizeObserver(updateSplitMeasurements);
      observer.observe(contentEl);
      observer.observe(sidebarEl);
      observers.push(observer);
    }

    window.addEventListener("resize", updateSplitMeasurements);
    return () => {
      window.removeEventListener("resize", updateSplitMeasurements);
      for (const observer of observers) {
        observer.disconnect();
      }
    };
  });

  $effect(() => {
    const nextRepoLoadKey = routeKey(route);
    applyRouteAnchor(route);
    if (nextRepoLoadKey !== repoLoadKey && nextRepoLoadKey !== repoLoadAliasKey) {
      repoLoadKey = nextRepoLoadKey;
      repoLoadAliasKey = "";
      void loadRoute(route);
      return;
    }
    const currentPath = untrack(() => store.getSelectedPath());
    if (route.path && route.path !== currentPath) {
      const generation = routeLoadGeneration + 1;
      routeLoadGeneration = generation;
      void syncRoutePath(route.path, generation);
    }
    const nextMode = routeViewMode(route);
    const currentMode = untrack(() => store.getViewMode());
    if (nextMode !== currentMode) {
      store.setViewMode(nextMode);
    }
  });

  function routeKey(value: RepoBrowserFeatureRoute): string {
    const refSHA = value.refSHA ?? (value.refType === "commit" ? (value.refName ?? "") : "");
    return [
      value.provider,
      value.platformHost ?? "",
      value.repoPath,
      value.refType ?? "",
      value.refName ?? "",
      refSHA,
    ].join("\0");
  }

  async function loadRoute(value: RepoBrowserFeatureRoute): Promise<void> {
    const requestedLoadKey = routeKey(value);
    const generation = routeLoadGeneration + 1;
    const selectionGeneration = nextPathSelectionGeneration();
    routeLoadGeneration = generation;
    store.setViewMode(routeViewMode(value));
    const requestedRef = routeRef(value);
    await store.loadRepo(repoRef(value), {
      ...(requestedRef ? { ref: requestedRef } : {}),
      path: value.path ?? null,
    });
    if (generation !== routeLoadGeneration || selectionGeneration !== pathSelectionGeneration) return;
    repoLoadKey = requestedLoadKey;
    repoLoadAliasKey = routeKeyWithSelectedRef(value);
    if (!value.path) {
      const initialPath = chooseRepoBrowserInitialPath(store.getTree());
      if (initialPath && initialPath !== store.getSelectedPath()) {
        await store.selectPath(initialPath);
        if (generation !== routeLoadGeneration || !pathSelectionStillCurrent(selectionGeneration, initialPath)) return;
      }
      if (initialPath) {
        pushRoute({ path: initialPath }, { replace: true });
      }
    }
    if (generation !== routeLoadGeneration || selectionGeneration !== pathSelectionGeneration) return;
    selectedPathRevealKey += 1;
  }

  function repoRef(value: RepoBrowserFeatureRoute) {
    return {
      provider: value.provider,
      ...(value.platformHost ? { platformHost: value.platformHost } : {}),
      owner: value.owner,
      name: value.name,
      repoPath: value.repoPath,
    };
  }

  function routeRef(value: RepoBrowserFeatureRoute): RepoBrowserRef | undefined {
    if (!value.refType && !value.refName && !value.refSHA) return undefined;
    const type = value.refType ?? "branch";
    return {
      type,
      name: value.refName ?? value.refSHA ?? "",
      sha: value.refSHA ?? (type === "commit" ? (value.refName ?? "") : ""),
      stale: false,
    };
  }

  function routeViewMode(value: RepoBrowserFeatureRoute): RepoBrowserViewMode {
    return value.mode ?? "source";
  }

  function routeKeyWithSelectedRef(value: RepoBrowserFeatureRoute): string {
    const ref = store.getSelectedRef();
    return routeKey({
      ...value,
      ...(ref ? {
        refType: ref.type,
        refName: ref.name,
        refSHA: ref.sha,
      } : {}),
    });
  }

  function pushRoute(
    update: RepoBrowserRouteUpdate = {},
    options?: { replace?: boolean },
  ): void {
    const ref = store.getSelectedRef();
    const path = update.path ?? store.getSelectedPath() ?? undefined;
    const mode = update.mode ?? store.getViewMode();
    onRouteChange(
      {
        provider: route.provider,
        ...(route.platformHost ? { platformHost: route.platformHost } : {}),
        owner: route.owner,
        name: route.name,
        repoPath: route.repoPath,
        ...(ref ? {
          refType: ref.type,
          refName: ref.name,
          refSHA: ref.sha,
        } : {}),
        ...(path ? { path } : {}),
        viewMode: mode,
        ...(update.anchor ? { anchor: update.anchor } : {}),
      },
      options,
    );
  }

  async function selectPath(path: string, options?: { replace?: boolean }): Promise<void> {
    const generation = nextPathSelectionGeneration();
    await store.selectPath(path);
    if (!pathSelectionStillCurrent(generation, path)) return;
    selectedPathRevealKey += 1;
    pushRoute({ path }, options);
  }

  async function syncRoutePath(path: string, generation: number): Promise<void> {
    const selectionGeneration = nextPathSelectionGeneration();
    await store.selectPath(path);
    if (generation !== routeLoadGeneration || !pathSelectionStillCurrent(selectionGeneration, path)) return;
    selectedPathRevealKey += 1;
  }

  function nextPathSelectionGeneration(): number {
    pathSelectionGeneration += 1;
    return pathSelectionGeneration;
  }

  function pathSelectionStillCurrent(generation: number, path: string): boolean {
    return generation === pathSelectionGeneration && store.getSelectedPath() === path;
  }

  async function selectRefByKey(key: string): Promise<boolean> {
    const ref = store.getRefs().find((candidate) => refKey(candidate) === key);
    if (!ref) return false;
    if (!(await store.selectRef(ref))) return false;
    selectedPathRevealKey += 1;
    repoLoadKey = routeKeyWithSelectedRef(route);
    repoLoadAliasKey = "";
    pushRoute({ path: store.getSelectedPath() ?? undefined });
    return true;
  }

  async function selectRefFromPicker(key: string): Promise<boolean | void> {
    refPickerError = "";
    if (selectedRef !== null && refKey(selectedRef) === key) return;
    if (refPickerSelectionInFlight) return false;
    refPickerSelectionInFlight = true;
    try {
      if (!(await selectRefByKey(key))) {
        refPickerError = "Couldn't load repository ref";
        return false;
      }
      refPickerQuery = "";
    } catch {
      refPickerError = "Couldn't load repository ref";
      return false;
    } finally {
      refPickerSelectionInFlight = false;
    }
  }

  function setRefPickerType(type: RefPickerType): void {
    refPickerType = type;
    refPickerError = "";
  }

  function setCategoryFilter(filter: DiffFileCategoryFilter): void {
    store.setFileCategoryFilter(filter);
  }

  function setViewMode(mode: RepoBrowserViewMode): void {
    store.setViewMode(mode);
    pushRoute({ mode }, { replace: true });
  }

  function refreshRepo(): void {
    repoLoadKey = "";
    void loadRoute(route);
  }

  function selectHistoryCommit(commit: RepoBrowserCommit): void {
    void store.selectCommit(commit.sha);
  }

  function updateSplitMeasurements(): void {
    const nextContentWidth = contentEl?.clientWidth ?? 0;
    historyRailVisible =
      typeof window === "undefined" || typeof window.matchMedia !== "function"
        ? true
        : window.matchMedia("(min-width: 901px)").matches;
    contentWidth = nextContentWidth;
    if (nextContentWidth <= 0) return;
    const nextFilesWidth = clampFilesWidth(filesWidth, nextContentWidth, historyWidth, historyRailVisible);
    if (nextFilesWidth !== filesWidth) {
      filesWidth = nextFilesWidth;
    }
    const nextHistoryWidth = clampHistoryWidth(historyWidth, nextContentWidth, nextFilesWidth, historyRailVisible);
    if (nextHistoryWidth !== historyWidth) {
      historyWidth = nextHistoryWidth;
    }
  }

  function railHandleCount(nextHistoryRailVisible = historyRailVisible): number {
    return nextHistoryRailVisible ? 2 : 1;
  }

  function maxFilesWidth(
    nextContentWidth = contentWidth,
    nextHistoryWidth = historyWidth,
    nextHistoryRailVisible = historyRailVisible,
  ): number {
    if (nextContentWidth <= 0) return Math.max(DEFAULT_FILES_WIDTH, MIN_RAIL_WIDTH);
    const visibleHistoryWidth = nextHistoryRailVisible ? nextHistoryWidth : 0;
    const availableForFiles =
      nextContentWidth - visibleHistoryWidth - railHandleCount(nextHistoryRailVisible) * RESIZE_HANDLE_WIDTH;
    if (availableForFiles <= 0) return MIN_RAIL_WIDTH;
    return Math.max(MIN_RAIL_WIDTH, availableForFiles - MIN_VIEWER_WIDTH);
  }

  function maxHistoryWidth(
    nextContentWidth = contentWidth,
    nextFilesWidth = filesWidth,
    nextHistoryRailVisible = historyRailVisible,
  ): number {
    if (!nextHistoryRailVisible) return Math.max(DEFAULT_HISTORY_WIDTH, MIN_RAIL_WIDTH);
    if (nextContentWidth <= 0) return Math.max(DEFAULT_HISTORY_WIDTH, MIN_RAIL_WIDTH);
    const availableForHistory =
      nextContentWidth - nextFilesWidth - railHandleCount(nextHistoryRailVisible) * RESIZE_HANDLE_WIDTH;
    if (availableForHistory <= 0) return MIN_RAIL_WIDTH;
    return Math.max(MIN_RAIL_WIDTH, availableForHistory - MIN_VIEWER_WIDTH);
  }

  function clampFilesWidth(
    width: number,
    nextContentWidth = contentWidth,
    nextHistoryWidth = historyWidth,
    nextHistoryRailVisible = historyRailVisible,
  ): number {
    const maxWidth = maxFilesWidth(nextContentWidth, nextHistoryWidth, nextHistoryRailVisible);
    const minWidth = Math.min(MIN_RAIL_WIDTH, maxWidth);
    return Math.max(minWidth, Math.min(maxWidth, width));
  }

  function clampHistoryWidth(
    width: number,
    nextContentWidth = contentWidth,
    nextFilesWidth = filesWidth,
    nextHistoryRailVisible = historyRailVisible,
  ): number {
    const maxWidth = maxHistoryWidth(nextContentWidth, nextFilesWidth, nextHistoryRailVisible);
    const minWidth = Math.min(MIN_RAIL_WIDTH, maxWidth);
    return Math.max(minWidth, Math.min(maxWidth, width));
  }

  function startFilesResize(): void {
    updateSplitMeasurements();
    filesWidth = clampFilesWidth(filesWidth);
    filesResizeStartWidth = filesWidth;
  }

  function resizeFiles(event: SplitResizeEvent): void {
    filesWidth = clampFilesWidth(filesResizeStartWidth + event.delta);
  }

  function startHistoryResize(): void {
    updateSplitMeasurements();
    historyWidth = clampHistoryWidth(historyWidth);
    historyResizeStartWidth = historyWidth;
  }

  function resizeHistory(event: SplitResizeEvent): void {
    historyWidth = clampHistoryWidth(historyResizeStartWidth - event.delta);
  }

  function toTreeEntry(file: SourceBrowserFileEntry): FileTreeEntry {
    const lastChanged = file.lastChanged;
    return {
      path: file.path,
      ...(lastChanged ? {
        decoration: formatRepoBrowserCommitAge(lastChanged.authored_at),
        decorationTitle: `${formatRepoBrowserCommitDate(lastChanged.authored_at)} · ${lastChanged.subject} (${lastChanged.sha.slice(0, 12)})`,
      } : {}),
    };
  }

  function findSelectedFile(
    files: readonly SourceBrowserFileEntry[],
    path: string | null,
  ): SourceBrowserFileEntry | null {
    if (!path) return null;
    return files.find((entry) => entry.path === path) ?? null;
  }

  function refLabel(ref: RepoBrowserRef | null): string {
    if (!ref) return "No ref";
    if (ref.type === "commit") return ref.sha.slice(0, 12);
    return ref.name || ref.sha.slice(0, 12);
  }

  function refKey(ref: RepoBrowserRef): string {
    return `${ref.type}\0${ref.name}\0${ref.sha}`;
  }

  function refOptionLabel(ref: RepoBrowserRef): string {
    const suffix = ref.sha ? ` ${ref.sha.slice(0, 8)}` : "";
    return `${ref.type}: ${ref.name || ref.sha.slice(0, 12)}${suffix}`;
  }

  function refPickerRefType(ref: RepoBrowserRef): RefPickerType | null {
    if (ref.type === "branch" || ref.type === "tag") return ref.type;
    return null;
  }

  function buildMarkdownIndex(files: readonly SourceBrowserFileEntry[]): FolderIndex {
    const byPath = new SvelteMap<string, string>();
    const byBasename = new SvelteMap<string, string[]>();
    for (const file of files) {
      if (!isRepoBrowserMarkdownPath(file.path)) continue;
      const lowerPath = file.path.toLowerCase();
      byPath.set(lowerPath, file.path);
      byPath.set(lowerPath.replace(/\.(md|mdx)$/i, ""), file.path);
      const base = lowerPath.split("/").at(-1)?.replace(/\.(md|mdx)$/i, "") ?? lowerPath;
      byBasename.set(base, [...(byBasename.get(base) ?? []), file.path]);
    }
    return { byPath, byBasename };
  }

  function markdownOptions(path: string) {
    return {
      folderID: route.repoPath,
      currentDocPath: path,
      index: markdownIndex,
      buildDocURL: (_folderID: string, relPath: string, anchor?: string) =>
        buildRepoBrowserRoute({
          ...repoRef(route),
          ...(selectedRef ? {
            refType: selectedRef.type,
            refName: selectedRef.name,
            refSHA: selectedRef.sha,
          } : {}),
          path: relPath,
          viewMode: "preview",
          ...(anchor ? { anchor } : {}),
        }),
      buildBlobURL: (_folderID: string, relPath: string) => assetURL(relPath),
      allowExternalImages: false,
      repoContext: repoRef(route),
    };
  }

  function assetURL(path: string): string {
    const params = new URLSearchParams();
    params.set("repo_path", route.repoPath);
    params.set("path", path);
    if (selectedRef?.sha) {
      params.set("ref_type", "commit");
      params.set("ref_sha", selectedRef.sha);
    }
    const hostPath = route.platformHost
      ? `/host/${encodeURIComponent(route.platformHost)}`
      : "";
    const endpointPath = `${hostPath}/repo/${encodeURIComponent(route.provider)}/${encodeURIComponent(route.owner)}/${encodeURIComponent(route.name)}/browser/asset`;
    const url = new URL(endpointPath.replace(/^\//, ""), withTrailingSlash(apiBaseURL));
    url.search = params.toString();
    return url.toString();
  }

  function withTrailingSlash(value: string): string {
    return value.endsWith("/") ? value : `${value}/`;
  }

  function initialMarkdownAnchor(): string | null {
    if (typeof window === "undefined") return null;
    const raw = window.location.hash.replace(/^#/, "");
    if (!raw) return null;
    try {
      return decodeURIComponent(raw);
    } catch {
      return raw;
    }
  }

  function openMarkdownDoc(path: string, anchor?: string): void {
    void (async () => {
      const generation = nextPathSelectionGeneration();
      if (path !== store.getSelectedPath()) {
        await store.selectPath(path);
        if (!pathSelectionStillCurrent(generation, path)) return;
        selectedPathRevealKey += 1;
      }
      if (!pathSelectionStillCurrent(generation, path)) return;
      routeAnchorKey = routeAnchorStateKey(path, anchor ?? null);
      pendingMarkdownAnchor = anchor ?? null;
      store.setViewMode("preview");
      pushRoute({ path, mode: "preview", anchor: anchor ?? null });
    })();
  }

  function buildForgeHref(
    value: RepoBrowserFeatureRoute,
    ref: RepoBrowserRef | null,
    path: string | null,
  ): string | null {
    if (!ref || !path) return null;
    const host = value.platformHost ?? providerDefaultHost(value.provider);
    if (!host) return null;
    const encodedRepo = value.repoPath.split("/").map(encodeURIComponent).join("/");
    const encodedPath = path.split("/").map(encodeURIComponent).join("/");
    const encodedRef = encodeURIComponent(ref.name || ref.sha);
    if (value.provider === "gitlab") {
      return `https://${host}/${encodedRepo}/-/blob/${encodedRef}/${encodedPath}`;
    }
    if (value.provider === "forgejo" || value.provider === "gitea") {
      const refKind = ref.type === "tag" ? "tag" : ref.type === "commit" ? "commit" : "branch";
      return `https://${host}/${encodedRepo}/src/${refKind}/${encodedRef}/${encodedPath}`;
    }
    return `https://${host}/${encodedRepo}/blob/${encodedRef}/${encodedPath}`;
  }

  function applyRouteAnchor(value: RepoBrowserFeatureRoute): void {
    const key = routeAnchorStateKey(value.path ?? null, value.anchor ?? null);
    if (key === routeAnchorKey) return;
    routeAnchorKey = key;
    pendingMarkdownAnchor = value.anchor ?? null;
  }

  function routeAnchorStateKey(path: string | null, anchor: string | null): string {
    return `${path ?? ""}\0${anchor ?? ""}`;
  }
</script>

{#snippet refPickerHeader()}
  <div class="repo-browser__ref-tabs" role="tablist" aria-label="Repository ref types">
    <button
      type="button"
      role="tab"
      aria-selected={refPickerType === "branch"}
      class:repo-browser__ref-tab--active={refPickerType === "branch"}
      onclick={() => setRefPickerType("branch")}
    >Branches {branchRefs.length}</button>
    <button
      type="button"
      role="tab"
      aria-selected={refPickerType === "tag"}
      class:repo-browser__ref-tab--active={refPickerType === "tag"}
      onclick={() => setRefPickerType("tag")}
    >Tags {tagRefs.length}</button>
  </div>
  {#if refPickerTruncated}
    <div class="repo-browser__ref-more">
      Showing first {refPickerOptions.length} of {refPickerFilteredRefs.length}
    </div>
  {/if}
{/snippet}

<section class="repo-browser" aria-label="Repository source browser">
  <header class="repo-browser__toolbar">
    <div class="repo-browser__identity">
      <span class="repo-browser__provider">{route.provider}</span>
      <span class="repo-browser__repo">{route.repoPath}</span>
      <span class="repo-browser__ref">{refLabel(selectedRef)}</span>
    </div>
    <div class="repo-browser__actions">
      <div class="repo-browser__ref-picker">
        <Typeahead
          options={refPickerOptions}
          value={selectedRef && refPickerRefType(selectedRef) ? refKey(selectedRef) : ""}
          fallbackLabel="No ref"
          placeholder="Search repository refs"
          triggerPrefix="Select repository ref:"
          emptyLabel={`No ${refPickerType === "branch" ? "branches" : "tags"} match`}
          loading={refPickerSelectionInFlight}
          loadingLabel="Loading repository ref…"
          error={refPickerError}
          header={refPickerHeader}
          remote
          onquery={(query) => (refPickerQuery = query)}
          onselect={selectRefFromPicker}
        />
      </div>
      <button class="repo-browser__icon-button" type="button" title="Refresh repository" onclick={refreshRepo}>
        <RefreshIcon size="15" strokeWidth="1.75" aria-hidden="true" />
      </button>
      {#if forgeHref}
        <a class="repo-browser__icon-button" href={forgeHref} target="_blank" rel="noreferrer" title="Open on forge">
          <ExternalLinkIcon size="15" strokeWidth="1.75" aria-hidden="true" />
        </a>
      {/if}
    </div>
  </header>

  <div class="repo-browser__content" bind:this={contentEl}>
    <aside
      class="repo-browser__sidebar"
      aria-label="Files"
      bind:this={sidebarEl}
      style:width={`${Math.round(filesWidth)}px`}
    >
      <div class="repo-browser__filter">
        <SearchInput
          bind:value={pathFilter}
          size="sm"
          block
          placeholder="Filter files"
          ariaLabel="Filter files"
        />
      </div>
      <div class="repo-browser__categories" aria-label="File category filters">
        {#each visibleCategoryOptions as option (option.value)}
          <button
            type="button"
            aria-pressed={store.getFileCategoryFilter() === option.value}
            class:repo-browser__category--active={store.getFileCategoryFilter() === option.value}
            onclick={() => setCategoryFilter(option.value)}
          >
            <span>{option.label}</span>
            <span>{categoryCounts[option.value]}</span>
          </button>
        {/each}
      </div>
      <div class="repo-browser__tree">
        <PierreFileTree
          files={null}
          entries={treeEntries}
          selectedPath={selectedPath}
          {selectedPathRevealKey}
          ariaLabel="Repository files"
          onSelect={(path) => void selectPath(path)}
        />
      </div>
    </aside>

    <SplitResizeHandle
      class="repo-browser__files-resize"
      ariaLabel="Resize file tree"
      orientation="horizontal"
      ariaValueMin={Math.min(MIN_RAIL_WIDTH, maxFilesWidth())}
      ariaValueMax={maxFilesWidth()}
      ariaValueNow={filesWidth}
      onResizeStart={startFilesResize}
      onResize={resizeFiles}
    />

    <main class="repo-browser__viewer" aria-label="Selected file">
      <div class="repo-browser__filebar">
        <div class="repo-browser__path">
          {#if selectedPath}
            {selectedPath}
          {:else}
            No file selected
          {/if}
        </div>
        <div class="repo-browser__filemeta">
          {#if selectedFile}
            <span>{formatRepoBrowserFileSize(selectedFile.size)}</span>
          {/if}
          {#if selectedIsMarkdown}
            <div class="repo-browser__segmented" aria-label="View mode">
              <button
                type="button"
                class:repo-browser__segment--active={viewMode === "source"}
                onclick={() => setViewMode("source")}
              >Source</button>
              <button
                type="button"
                class:repo-browser__segment--active={viewMode === "preview"}
                disabled={!canPreview}
                onclick={() => setViewMode("preview")}
              >Preview</button>
            </div>
          {/if}
        </div>
      </div>

      {#if store.isLoading() || store.isBlobLoading()}
        <div class="repo-browser__state">
          <SpinnerIcon size="18" strokeWidth="2" aria-hidden="true" />
          Loading
        </div>
      {:else if store.getError()}
        <div class="repo-browser__state repo-browser__state--error">{store.getError()}</div>
      {:else if !selectedBlob}
        <div class="repo-browser__state">Select a file</div>
      {:else if selectedBlob.too_large}
        <div class="repo-browser__state">File is too large to display</div>
      {:else if selectedBlob.binary}
        <div class="repo-browser__state">Binary file cannot be previewed</div>
      {:else if viewMode === "preview" && selectedIsMarkdown}
        <article class="repo-browser__markdown">
          <DocMarkdownView
            source={selectedBlob.content}
            options={markdownOptions(selectedBlob.path)}
            onSelectDoc={(path, anchor) => openMarkdownDoc(path, anchor)}
            scrollToAnchor={pendingMarkdownAnchor}
            onAnchorConsumed={() => (pendingMarkdownAnchor = null)}
          />
        </article>
      {:else}
        <div class="repo-browser__source repo-browser__source--pierre">
          <PierreFileContents path={selectedBlob.path} contents={selectedBlob.content} />
        </div>
      {/if}
    </main>

    <SplitResizeHandle
      class="repo-browser__history-resize"
      ariaLabel="Resize file history"
      orientation="horizontal"
      ariaValueMin={Math.min(MIN_RAIL_WIDTH, maxHistoryWidth())}
      ariaValueMax={maxHistoryWidth()}
      ariaValueNow={historyWidth}
      onResizeStart={startHistoryResize}
      onResize={resizeHistory}
    />

    <aside class="repo-browser__history" aria-label="File history" style:width={`${Math.round(historyWidth)}px`}>
      <header class="repo-browser__history-header">
        <span>History</span>
        <span>{store.getFileHistory().length}</span>
      </header>
      <div class="repo-browser__history-list">
        {#each store.getFileHistory() as commit (commit.sha)}
          <button
            type="button"
            class:repo-browser__history-row--active={store.getSelectedCommit()?.sha === commit.sha}
            onclick={() => selectHistoryCommit(commit)}
          >
            <span>{commit.subject}</span>
            <span>{commit.author_name} · {formatRepoBrowserCommitDate(commit.authored_at)}</span>
          </button>
        {/each}
      </div>
      {#if selectedCommitDetail}
        <section class="repo-browser__commit-detail" aria-label="Selected commit">
          <div class="repo-browser__commit-sha">{selectedCommitDetail.sha.slice(0, 12)}</div>
          <h2>{selectedCommitDetail.subject}</h2>
          <p>
            {selectedCommitDetail.author_name} · {formatRepoBrowserCommitDate(selectedCommitDetail.authored_at)}
          </p>
          {#if selectedCommitDetail.body}
            <pre>{selectedCommitDetail.body}</pre>
          {/if}
        </section>
      {/if}
    </aside>
  </div>
</section>

<style>
  .repo-browser {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: var(--bg-primary);
  }

  .repo-browser__toolbar {
    min-height: 46px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 8px 12px;
    border-bottom: thin solid var(--border-default);
    background: var(--bg-surface);
  }

  .repo-browser__identity,
  .repo-browser__actions,
  .repo-browser__filemeta,
  .repo-browser__history-header {
    display: flex;
    align-items: center;
    min-width: 0;
  }

  .repo-browser__identity {
    gap: 8px;
    font-size: var(--font-size-sm);
  }

  .repo-browser__provider {
    color: var(--text-muted);
    text-transform: uppercase;
    font-size: var(--font-size-2xs);
    font-weight: 700;
  }

  .repo-browser__repo {
    color: var(--text-primary);
    font-weight: 650;
  }

  .repo-browser__ref,
  .repo-browser__filemeta {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .repo-browser__actions {
    gap: 8px;
    flex: 0 0 auto;
  }

  .repo-browser__icon-button {
    width: 30px;
    height: 30px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: thin solid var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    background: var(--bg-surface);
    text-decoration: none;
  }

  .repo-browser__icon-button:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .repo-browser__ref-picker {
    width: min(280px, 38vw);
    min-width: 210px;
    --typeahead-min-width: 210px;
    --typeahead-max-width: min(280px, 38vw);
    --typeahead-control-height: 30px;
    --typeahead-control-font-size: var(--font-size-sm);
  }

  .repo-browser__ref-tabs {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-1);
  }

  .repo-browser__ref-tabs button {
    min-height: 26px;
    border: 0;
    border-radius: 3px;
    color: var(--text-secondary);
    background: transparent;
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .repo-browser__ref-tabs button:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .repo-browser__ref-tab--active {
    color: var(--text-primary) !important;
    background: var(--bg-inset) !important;
  }

  .repo-browser__ref-more {
    padding: 5px 8px 3px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .repo-browser__content {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    overflow: hidden;
  }

  .repo-browser__sidebar,
  .repo-browser__history {
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: var(--bg-surface);
  }

  .repo-browser__sidebar {
    flex: 0 0 auto;
    border-right: thin solid var(--border-default);
  }

  .repo-browser__history {
    flex: 0 0 auto;
    border-left: thin solid var(--border-default);
  }

  :global(.repo-browser__files-resize),
  :global(.repo-browser__history-resize) {
    background: var(--border-default);
  }

  .repo-browser__filter {
    padding: 10px 10px 6px;
  }

  .repo-browser__categories {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 0 10px 10px;
  }

  .repo-browser__categories button {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    min-height: 24px;
    padding: 0 7px;
    border: thin solid var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    background: var(--bg-surface);
    font-size: var(--font-size-xs);
  }

  .repo-browser__categories button:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .repo-browser__categories button.repo-browser__category--active {
    border-color: var(--accent-blue);
    color: var(--accent-blue);
    background: var(--bg-surface-hover);
    box-shadow:
      inset 0 0 0 1px color-mix(in srgb, var(--accent-blue) 55%, transparent),
      0 0 0 1px color-mix(in srgb, var(--accent-blue) 18%, transparent);
  }

  .repo-browser__tree {
    flex: 1 1 auto;
    min-height: 0;
    padding: 0 4px 8px;
  }

  .repo-browser__viewer {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: var(--bg-primary);
  }

  .repo-browser__filebar {
    min-height: 42px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 7px 12px;
    border-bottom: thin solid var(--border-default);
    background: var(--bg-surface);
  }

  .repo-browser__path {
    min-width: 0;
    overflow: hidden;
    color: var(--text-primary);
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .repo-browser__filemeta {
    flex: 0 0 auto;
    gap: var(--space-4);
  }

  .repo-browser__segmented {
    display: inline-flex;
    padding: 2px;
    border: thin solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
  }

  .repo-browser__segmented button {
    min-height: 24px;
    padding: 0 8px;
    border: 0;
    border-radius: calc(var(--radius-sm) - 1px);
    color: var(--text-secondary);
    background: transparent;
    font-size: var(--font-size-xs);
  }

  .repo-browser__segmented button:disabled {
    color: var(--text-muted);
  }

  .repo-browser__segment--active {
    color: var(--text-primary) !important;
    background: var(--bg-surface) !important;
  }

  .repo-browser__source,
  .repo-browser__markdown {
    flex: 1 1 auto;
    min-height: 0;
    margin: 0;
    overflow: auto;
  }

  .repo-browser__source {
    padding: 14px 16px;
    color: var(--text-primary);
    background: var(--bg-primary);
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    line-height: 1.55;
    tab-size: 2;
  }

  .repo-browser__source--pierre {
    padding: 0;
  }

  .repo-browser__source--pierre :global(.pierre-file-contents) {
    padding: 14px 16px;
  }

  .repo-browser__markdown {
    padding: 18px 24px 40px;
  }

  .repo-browser__state {
    flex: 1 1 auto;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .repo-browser__state--error {
    color: var(--accent-red);
  }

  .repo-browser__history-header {
    justify-content: space-between;
    min-height: 38px;
    padding: 0 12px;
    border-bottom: thin solid var(--border-default);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 650;
  }

  .repo-browser__history-list {
    flex: 0 0 auto;
    max-height: 48%;
    overflow: auto;
    border-bottom: thin solid var(--border-default);
  }

  .repo-browser__history-list button {
    width: 100%;
    display: grid;
    gap: var(--space-1);
    padding: 9px 12px;
    border: 0;
    border-bottom: thin solid var(--border-muted);
    color: var(--text-primary);
    background: transparent;
    text-align: left;
  }

  .repo-browser__history-list button:hover,
  .repo-browser__history-row--active {
    background: var(--bg-surface-hover) !important;
  }

  .repo-browser__history-list button span:first-child {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .repo-browser__history-list button span:last-child,
  .repo-browser__commit-detail p {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .repo-browser__commit-detail {
    flex: 1 1 auto;
    min-height: 0;
    padding: 12px;
    overflow: auto;
  }

  .repo-browser__commit-detail h2 {
    margin: 5px 0 5px;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    line-height: 1.35;
  }

  .repo-browser__commit-detail p {
    margin: 0 0 10px;
  }

  .repo-browser__commit-detail pre {
    margin: 0;
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    line-height: 1.45;
    white-space: pre-wrap;
  }

  .repo-browser__commit-sha {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  @media (max-width: 900px) {
    :global(.repo-browser__history-resize),
    .repo-browser__history {
      display: none;
    }

  }

  @media (max-width: 760px) {
    :global(.repo-browser__files-resize),
    .repo-browser__sidebar {
      display: none;
    }
  }
</style>
