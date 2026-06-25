<script lang="ts">
  import { tick, untrack } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import {
    buildRepoBrowserRoute,
    createRepoBrowserStore,
    diffFileCategoryOptions,
    PierreFileTree,
    type DiffFileCategoryFilter,
    type FileTreeEntry,
    type MiddlemanClient,
    type RepoBrowserRouteRef,
    type RepoBrowserViewMode,
    type SourceBrowserFileEntry,
  } from "@middleman/ui";
  import type { RepoBrowserCommit, RepoBrowserRef } from "@middleman/ui/api/types";
  import { providerDefaultHost } from "@middleman/ui/api/provider-routes";
  import DocMarkdownView from "../../components/docs/DocMarkdownView.svelte";
  import { ChevronDownIcon, RefreshIcon, ExternalLinkIcon, SpinnerIcon } from "../../icons";
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

  let { client, route, onRouteChange }: Props = $props();

  // svelte-ignore state_referenced_locally
  const store = createRepoBrowserStore({ client });

  let repoLoadKey = "";
  let routeLoadGeneration = 0;
  let pathSelectionGeneration = 0;
  let routeAnchorKey = "";
  let pathFilter = $state("");
  let selectedPathRevealKey = $state(0);
  let pendingMarkdownAnchor = $state(initialMarkdownAnchor());
  let refPickerOpen = $state(false);
  let refPickerQuery = $state("");
  let refPickerType = $state<RefPickerType>("branch");
  let refPickerHighlightIndex = $state(0);
  let refPickerInputEl: HTMLInputElement | null = $state(null);
  let refPickerRootEl: HTMLDivElement | null = $state(null);
  let refPickerSelectionInFlight = false;
  const refPickerListID = `repo-browser-ref-${Math.random().toString(36).slice(2)}`;
  const refPickerRenderLimit = 100;

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
  const filteredRefs = $derived.by(() => filterRefs(refPickerType === "branch" ? branchRefs : tagRefs, refPickerQuery));
  const visibleFilteredRefs = $derived(filteredRefs.slice(0, refPickerRenderLimit));

  $effect(() => {
    const nextRepoLoadKey = routeKey(route);
    applyRouteAnchor(route);
    if (nextRepoLoadKey !== repoLoadKey) {
      repoLoadKey = nextRepoLoadKey;
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
    if (!value.path) {
      const initialPath = chooseRepoBrowserInitialPath(store.getTree());
      if (initialPath && initialPath !== store.getSelectedPath()) {
        await store.selectPath(initialPath);
        if (generation !== routeLoadGeneration || !pathSelectionStillCurrent(selectionGeneration, initialPath)) return;
      }
      if (initialPath) {
        repoLoadKey = routeKeyWithSelectedRef(value);
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

  async function selectRefByKey(key: string): Promise<void> {
    const ref = store.getRefs().find((candidate) => refKey(candidate) === key);
    if (!ref) return;
    await store.selectRef(ref);
    selectedPathRevealKey += 1;
    repoLoadKey = routeKeyWithSelectedRef(route);
    pushRoute({ path: store.getSelectedPath() ?? undefined });
  }

  async function openRefPicker(): Promise<void> {
    refPickerQuery = "";
    refPickerType = initialRefPickerType();
    refPickerHighlightIndex = 0;
    refPickerOpen = true;
    await tick();
    refPickerInputEl?.focus();
  }

  function closeRefPicker(): void {
    refPickerOpen = false;
    refPickerQuery = "";
    refPickerHighlightIndex = 0;
  }

  async function selectRefFromPicker(ref: RepoBrowserRef): Promise<void> {
    if (selectedRef !== null && refKey(selectedRef) === refKey(ref)) {
      closeRefPicker();
      return;
    }
    if (refPickerSelectionInFlight) return;
    refPickerSelectionInFlight = true;
    closeRefPicker();
    try {
      await selectRefByKey(refKey(ref));
    } finally {
      refPickerSelectionInFlight = false;
    }
  }

  function setRefPickerType(type: RefPickerType): void {
    refPickerType = type;
    refPickerHighlightIndex = 0;
    void tick().then(() => refPickerInputEl?.focus());
  }

  function handleRefPickerInput(): void {
    refPickerHighlightIndex = 0;
  }

  function handleRefPickerKeydown(event: KeyboardEvent): void {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      refPickerHighlightIndex = Math.min(refPickerHighlightIndex + 1, Math.max(visibleFilteredRefs.length - 1, 0));
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      refPickerHighlightIndex = Math.max(refPickerHighlightIndex - 1, 0);
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      const ref = visibleFilteredRefs[refPickerHighlightIndex];
      if (ref) void selectRefFromPicker(ref);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      closeRefPicker();
    }
  }

  function handleRefPickerBlur(event: FocusEvent): void {
    const related = event.relatedTarget as Node | null;
    if (refPickerRootEl && related && refPickerRootEl.contains(related)) return;
    closeRefPicker();
  }

  function initialRefPickerType(): RefPickerType {
    if (selectedRef?.type === "tag") return "tag";
    if (selectedRef?.type === "branch") return "branch";
    return branchRefs.length > 0 ? "branch" : "tag";
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

  function filterRefs(refs: readonly RepoBrowserRef[], query: string): RepoBrowserRef[] {
    const q = query.trim().toLowerCase();
    if (!q) return [...refs];
    return refs.filter((ref) =>
      [ref.type, ref.name, ref.sha, refOptionLabel(ref)].some((part) => part.toLowerCase().includes(q)),
    );
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

<section class="repo-browser" aria-label="Repository source browser">
  <header class="repo-browser__toolbar">
    <div class="repo-browser__identity">
      <span class="repo-browser__provider">{route.provider}</span>
      <span class="repo-browser__repo">{route.repoPath}</span>
      <span class="repo-browser__ref">{refLabel(selectedRef)}</span>
    </div>
    <div class="repo-browser__actions">
      <div class="repo-browser__ref-picker typeahead" bind:this={refPickerRootEl}>
        {#if refPickerOpen}
          <input
            bind:this={refPickerInputEl}
            class="typeahead-input"
            role="combobox"
            aria-label="Search repository refs"
            aria-expanded="true"
            aria-controls={refPickerListID}
            aria-autocomplete="list"
            type="text"
            bind:value={refPickerQuery}
            placeholder="Search refs"
            autocomplete="off"
            oninput={handleRefPickerInput}
            onkeydown={handleRefPickerKeydown}
            onblur={handleRefPickerBlur}
          />
          <div
            class="typeahead-list repo-browser__ref-popover"
            data-surface="solid"
          >
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
            <ul
              id={refPickerListID}
              class="repo-browser__ref-list"
              role="listbox"
              tabindex="-1"
              aria-label="Repository ref options"
            >
              {#each visibleFilteredRefs as ref, index (refKey(ref))}
                <li
                  class="typeahead-option"
                  class:highlighted={index === refPickerHighlightIndex}
                  class:selected={selectedRef !== null && refKey(ref) === refKey(selectedRef)}
                  role="option"
                  aria-selected={selectedRef !== null && refKey(ref) === refKey(selectedRef)}
                  tabindex="-1"
                  onclick={() => void selectRefFromPicker(ref)}
                  onmousedown={() => void selectRefFromPicker(ref)}
                  onkeydown={(event) => {
                    if (event.key === "Enter" || event.key === " ") void selectRefFromPicker(ref);
                  }}
                  onmouseenter={() => (refPickerHighlightIndex = index)}
                >
                  <span class="option-label">{ref.type}: {ref.name || ref.sha.slice(0, 12)}</span>
                  {#if ref.sha}
                    <span class="option-meta">{ref.sha.slice(0, 8)}</span>
                  {/if}
                </li>
              {:else}
                <li class="typeahead-empty">No {refPickerType === "branch" ? "branches" : "tags"} match</li>
              {/each}
            </ul>
            {#if filteredRefs.length > visibleFilteredRefs.length}
              <div class="repo-browser__ref-more">Showing first {visibleFilteredRefs.length} of {filteredRefs.length}</div>
            {/if}
          </div>
        {:else}
          <button
            class="typeahead-trigger"
            type="button"
            aria-label={`Select repository ref: ${selectedRef ? refOptionLabel(selectedRef) : "No ref"}`}
            aria-haspopup="listbox"
            onclick={() => void openRefPicker()}
          >
            <span class="typeahead-value">
              <span>{selectedRef ? refOptionLabel(selectedRef) : "No ref"}</span>
            </span>
            <ChevronDownIcon size={12} strokeWidth={2} aria-hidden="true" />
          </button>
        {/if}
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

  <div class="repo-browser__content">
    <aside class="repo-browser__sidebar" aria-label="Files">
      <div class="repo-browser__filter">
        <input
          type="search"
          placeholder="Filter files"
          aria-label="Filter files"
          bind:value={pathFilter}
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

    <aside class="repo-browser__history" aria-label="File history">
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

  .repo-browser__filter input {
    height: 30px;
    border: thin solid var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    background: var(--bg-inset);
    font: inherit;
    font-size: var(--font-size-sm);
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
    position: relative;
    width: min(280px, 38vw);
    min-width: 210px;
  }

  .typeahead-trigger,
  .typeahead-input {
    width: 100%;
    height: 30px;
    box-sizing: border-box;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
  }

  .typeahead-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: space-between;
    gap: 6px;
    padding: 0 8px;
    overflow: hidden;
    color: var(--text-secondary);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    cursor: pointer;
    text-align: left;
  }

  .typeahead-trigger:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
    border-color: var(--border-default);
  }

  .typeahead-value {
    min-width: 0;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .typeahead-input {
    padding: 0 8px;
    color: var(--text-primary);
    background: var(--bg-surface);
    border: 1px solid var(--accent-blue);
    outline: none;
    box-shadow: 0 0 0 3px var(--accent-blue-soft);
  }

  .typeahead-list {
    position: absolute;
    top: calc(100% + 3px);
    right: 0;
    z-index: 80;
    width: min(380px, calc(100vw - 24px));
    max-height: min(360px, 60vh);
    overflow-y: auto;
    padding: 3px;
    color: var(--text-secondary);
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    box-shadow: var(--shadow-lg);
  }

  .repo-browser__ref-tabs {
    position: sticky;
    top: -3px;
    z-index: 1;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 3px;
    padding: 3px 3px 5px;
    background: var(--bg-surface);
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

  .repo-browser__ref-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .typeahead-option {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    min-height: 28px;
    padding: 5px 8px;
    border-radius: 3px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.2;
    cursor: pointer;
  }

  .typeahead-option.highlighted {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .typeahead-option.selected {
    color: var(--accent-blue);
    font-weight: 600;
  }

  .option-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .option-meta {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
  }

  .typeahead-empty,
  .repo-browser__ref-more {
    padding: 7px 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .repo-browser__content {
    flex: 1 1 auto;
    min-height: 0;
    display: grid;
    grid-template-columns: minmax(260px, 340px) minmax(0, 1fr) minmax(250px, 320px);
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
    border-right: thin solid var(--border-default);
  }

  .repo-browser__history {
    border-left: thin solid var(--border-default);
  }

  .repo-browser__filter {
    padding: 10px 10px 6px;
  }

  .repo-browser__filter input {
    width: 100%;
    padding: 0 9px;
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
    gap: 5px;
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
    gap: 10px;
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
    gap: 3px;
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

  @media (max-width: 980px) {
    .repo-browser__content {
      grid-template-columns: minmax(220px, 300px) minmax(0, 1fr);
    }

    .repo-browser__history {
      display: none;
    }
  }

  @media (max-width: 720px) {
    .repo-browser__content {
      grid-template-columns: 1fr;
    }

    .repo-browser__sidebar {
      display: none;
    }
  }
</style>
