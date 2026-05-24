<script lang="ts">
  import { FileDiff } from "@pierre/diffs";
  import type {
    DiffLineAnnotation,
    ExpansionDirections,
    FileContents,
    FileDiffMetadata,
    FileDiffOptions,
    SelectedLineRange,
    ThemeTypes,
  } from "@pierre/diffs";
  import { mount, onMount, unmount } from "svelte";
  import type { DiffFile as DiffFileType } from "../../api/types.js";
  import type { DiffReviewDraftComment } from "../../stores/diff-review-draft.svelte.js";
  import type { DiffReviewLineRange } from "../../stores/diff-review-draft.svelte.js";
  import { STORES_KEY, getStores } from "../../context.js";

  const stores = getStores();
  const diffStore = stores.diff;
  const diffReviewDraft = stores.diffReviewDraft;
  import DiffInlineCommentComposer from "./DiffInlineCommentComposer.svelte";
  import DiffReviewDraftInlineComment from "./DiffReviewDraftInlineComment.svelte";
  import DiffReviewThreadInlineComment from "./DiffReviewThreadInlineComment.svelte";
  import DiffRichPreview from "./DiffRichPreview.svelte";
  import DiffStats from "../shared/DiffStats.svelte";
  import {
    reviewThreadTargetLine,
    reviewThreadTargetSide,
    type ReviewThread,
  } from "./review-thread-context.js";
  import { appThemeType, parsePierreFileDiff } from "./pierre-diff.js";

  interface Props {
    file: DiffFileType;
    provider: string;
    platformHost?: string | undefined;
    owner: string;
    name: string;
    repoPath: string;
    number: number;
    richPreviewEnabled?: boolean;
    reviewEnabled?: boolean;
    diffHeadSHA?: string | undefined;
    nativeMultilineRanges?: boolean;
    reviewThreads?: ReviewThread[];
  }

  const {
    file,
    provider,
    platformHost,
    owner,
    name,
    repoPath,
    number,
    richPreviewEnabled = true,
    reviewEnabled = false,
    diffHeadSHA = undefined,
    nativeMultilineRanges = false,
    reviewThreads = [],
  }: Props = $props();

  const collapsed = $derived(diffStore.isFileCollapsed(owner, name, number, file.path));
  const richPreview = $derived(diffStore.getRichPreview());
  const filePreviewGeneration = $derived(diffStore.getFilePreviewGeneration());
  const showRichPreview = $derived(
    richPreviewEnabled && richPreview && supportsRichPreview(file.path),
  );
  const richPreviewKey = $derived(`${file.path}:${filePreviewGeneration}`);
  const fileDraftComments = $derived(
    diffReviewDraft.getComments().filter((comment) => comment.path === file.path),
  );
  const fileReviewThreads = $derived(
    reviewThreads.filter((thread) => threadMatchesFile(thread)),
  );

  // Track viewport visibility so off-screen files skip expensive tokenization
  // on whitespace toggles and theme switches. Starts false so the initial
  // render on large diffs doesn't eagerly tokenize every file before the
  // IntersectionObserver reports visibility — the first observer callback
  // fires synchronously for on-screen files.
  let fileEl: HTMLDivElement | undefined = $state();
  let pierreDiffEl: HTMLDivElement | undefined = $state();
  let inViewport = $state(false);
  type ReviewSide = "left" | "right";
  type PierreSide = "deletions" | "additions";
  type ReviewLineRef = {
    side: ReviewSide;
    order: number;
    hunkIndex: number;
    line: number;
    oldLine?: number | undefined;
    newLine?: number | undefined;
    lineType: "context" | "add" | "delete";
  };
  type DiffAnnotation =
    | { kind: "draft"; id: string; comment: DiffReviewDraftComment }
    | { kind: "thread"; id: string; thread: ReviewThread }
    | { kind: "composer"; id: string; range: DiffReviewLineRange };

  let pierreDiff: FileDiff<DiffAnnotation> | undefined;
  let demandContextHandlerRoot: ShadowRoot | undefined;
  let fullContext: { oldFile: FileContents; newFile: FileContents } | undefined = $state();
  let contextLoadPromise: Promise<{ oldFile: FileContents; newFile: FileContents }> | undefined;
  let contextError: string | null = $state(null);
  let themeType = $state<ThemeTypes>(appThemeType());
  let selectedRange = $state<SelectedLineRange | null>(null);
  let composerRange = $state<DiffReviewLineRange | null>(null);
  const canLoadMoreContext = $derived(
    !file.is_binary && !showRichPreview && !fullContext && hasCollapsedContext(file),
  );
  const pierreFile = $derived.by<FileDiffMetadata | undefined>(() => {
    if (file.is_binary || showRichPreview) return undefined;
    return parsePierreFileDiff(file);
  });
  const selectableLineRefs = $derived.by(() => ({
    left: selectableLines("left"),
    right: selectableLines("right"),
  }));
  const lineAnnotations = $derived.by<DiffLineAnnotation<DiffAnnotation>[]>(() => {
    const annotations: DiffLineAnnotation<DiffAnnotation>[] = [];
    if (reviewEnabled) {
      for (const comment of fileDraftComments) {
        annotations.push({
          side: pierreSide(commentSide(comment)),
          lineNumber: comment.line,
          metadata: { kind: "draft", id: comment.id, comment },
        });
      }
    }
    for (const thread of fileReviewThreads) {
      if (!threadMatchesCurrentDiff(thread) || thread.line_type === "file") continue;
      annotations.push({
        side: pierreSide(reviewThreadTargetSide(thread)),
        lineNumber: reviewThreadTargetLine(thread),
        metadata: { kind: "thread", id: thread.id, thread },
      });
    }
    if (reviewEnabled && composerRange) {
      annotations.push({
        side: pierreSide(composerRange.side),
        lineNumber: composerRange.line,
        metadata: {
          kind: "composer",
          id: `composer:${composerRange.side}:${composerRange.line}`,
          range: composerRange,
        },
      });
    }
    return annotations;
  });

  onMount(() => {
    let observer: IntersectionObserver | undefined;
    let themeObserver: MutationObserver | undefined;
    // Guard for jsdom / SSR-ish test environments where IntersectionObserver
    // is not provided — treat the file as visible so rendering still runs.
    if (typeof IntersectionObserver === "undefined") {
      inViewport = true;
    } else if (fileEl) {
      observer = new IntersectionObserver(
        (entries) => { inViewport = entries[0]!.isIntersecting; },
        { rootMargin: "200px 0px" },
      );
      observer.observe(fileEl);
    }

    if (typeof MutationObserver !== "undefined") {
      themeObserver = new MutationObserver(() => {
        themeType = appThemeType();
      });
      themeObserver.observe(document.documentElement, {
        attributeFilter: ["class"],
      });
    }

    return () => {
      observer?.disconnect();
      themeObserver?.disconnect();
      demandContextHandlerRoot?.removeEventListener("click", handleDemandContextClick, {
        capture: true,
      });
      demandContextHandlerRoot = undefined;
      contextLoadPromise = undefined;
      pierreDiff?.cleanUp();
      pierreDiff = undefined;
    };
  });

  const pierreOptions = $derived<FileDiffOptions<DiffAnnotation>>({
    diffStyle: "unified",
    diffIndicators: "bars",
    disableFileHeader: true,
    enableGutterUtility: reviewEnabled && !!diffHeadSHA,
    enableLineSelection: reviewEnabled && !!diffHeadSHA,
    hunkSeparators: "line-info",
    lineDiffType: "word",
    lineHoverHighlight: reviewEnabled ? "both" : "disabled",
    onGutterUtilityClick: handlePierreSelection,
    onLineSelected: handlePierreSelection,
    renderAnnotation,
    overflow: diffStore.getWordWrap() ? "wrap" : "scroll",
    theme: { dark: "pierre-dark", light: "pierre-light" },
    themeType,
    expansionLineCount: 40,
    tokenizeMaxLineLength: 2_000,
    unsafeCSS: `
      :host {
        display: block;
        font-family: var(--font-mono);
        --diffs-font-family: var(--font-mono);
      }
      pre {
        margin: 0;
        border-radius: 0;
      }
      [data-separator='line-info'] {
        color: var(--diff-text-muted);
      }
      [data-expand-button] {
        cursor: pointer;
      }
    `,
  });

  $effect(() => {
    if (!pierreDiffEl || collapsed || !inViewport || !pierreFile) return;
    pierreDiff ??= new FileDiff<DiffAnnotation>(pierreOptions);
    pierreDiff.setOptions(pierreOptions);
    if (fullContext) {
      pierreDiff.render({
        fileContainer: pierreDiffEl,
        oldFile: fullContext.oldFile,
        newFile: fullContext.newFile,
        forceRender: true,
        lineAnnotations,
      });
    } else {
      pierreDiff.render({
        fileContainer: pierreDiffEl,
        fileDiff: pierreFile,
        forceRender: true,
        lineAnnotations,
      });
      installDemandContextHandler();
    }
    pierreDiff.setSelectedLines(selectedRange);
  });

  $effect(() => {
    if (!collapsed && pierreDiff && pierreFile) {
      pierreDiff.setThemeType(themeType);
    }
  });

  $effect(() => {
    pierreDiff?.setSelectedLines(selectedRange);
  });

  function toggle(): void {
    diffStore.toggleFileCollapsed(owner, name, number, file.path);
  }

  function installDemandContextHandler(): void {
    const root = pierreDiffEl?.shadowRoot;
    if (!root || root === demandContextHandlerRoot) return;
    demandContextHandlerRoot?.removeEventListener("click", handleDemandContextClick, {
      capture: true,
    });
    demandContextHandlerRoot = root;
    root.addEventListener("click", handleDemandContextClick, { capture: true });
  }

  function handleDemandContextClick(event: Event): void {
    if (fullContext) return;
    const button = closestFromEvent(event, "[data-expand-button]");
    if (!button) return;
    const separator = button.closest("[data-separator][data-expand-index]");
    const hunkIndex = Number(separator?.getAttribute("data-expand-index"));
    if (!Number.isFinite(hunkIndex)) return;

    event.preventDefault();
    event.stopImmediatePropagation();
    const direction = expansionDirection(button);
    void loadFullContext()
      .then(() => {
        pierreDiff?.expandHunk(hunkIndex, direction);
      })
      .catch((err: unknown) => {
        contextError = err instanceof Error ? err.message : String(err);
      });
  }

  function closestFromEvent(event: Event, selector: string): Element | null {
    for (const target of event.composedPath()) {
      if (target instanceof Element) {
        const match = target.closest(selector);
        if (match) return match;
      }
    }
    return null;
  }

  function expansionDirection(button: Element): ExpansionDirections {
    if (button.hasAttribute("data-expand-up")) return "up";
    if (button.hasAttribute("data-expand-down")) return "down";
    return "both";
  }

  async function loadFullContext(): Promise<{ oldFile: FileContents; newFile: FileContents }> {
    if (fullContext) return fullContext;
    contextLoadPromise ??= fetchFullContext();
    fullContext = await contextLoadPromise;
    return fullContext;
  }

  async function fetchFullContext(): Promise<{ oldFile: FileContents; newFile: FileContents }> {
    contextError = null;
    const [oldContents, newContents] = await Promise.all([
      file.status === "added" ? Promise.resolve("") : loadDiffText("old"),
      file.status === "deleted" ? Promise.resolve("") : loadDiffText("new"),
    ]);
    return {
      oldFile: {
        name: file.old_path || file.path,
        contents: oldContents,
        cacheKey: `middleman:${file.path}:old`,
      },
      newFile: {
        name: file.path,
        contents: newContents,
        cacheKey: `middleman:${file.path}:new`,
      },
    };
  }

  async function loadDiffText(side: "old" | "new"): Promise<string> {
    const preview = await diffStore.loadFilePreview(owner, name, number, file.path, side);
    return decodePreviewText(preview.content);
  }

  function decodePreviewText(content: string): string {
    const binary = atob(content);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
      bytes[i] = binary.charCodeAt(i);
    }
    return new TextDecoder("utf-8", { fatal: false }).decode(bytes);
  }

  function displayPath(f: DiffFileType): string {
    if (f.status === "renamed" && f.old_path !== f.path) {
      return `${f.old_path} -> ${f.path}`;
    }
    return f.path;
  }

  function supportsRichPreview(path: string): boolean {
    const idx = path.lastIndexOf(".");
    const ext = idx >= 0 ? path.slice(idx).toLowerCase() : "";
    return [
      ".avif",
      ".gif",
      ".jpeg",
      ".jpg",
      ".markdown",
      ".md",
      ".mdown",
      ".mkd",
      ".pdf",
      ".png",
      ".svg",
      ".webp",
    ].includes(ext);
  }

  function threadMatchesFile(thread: ReviewThread): boolean {
    return thread.path === file.path ||
      thread.path === file.old_path ||
      (!!thread.old_path && !!file.old_path && thread.old_path === file.old_path);
  }

  function threadMatchesCurrentDiff(thread: ReviewThread): boolean {
    return !thread.diff_head_sha || !diffHeadSHA || thread.diff_head_sha === diffHeadSHA;
  }

  function lineMatchesReviewThread(
    line: DiffFileType["hunks"][number]["lines"][number],
    thread: ReviewThread,
  ): boolean {
    if (!threadMatchesCurrentDiff(thread)) return false;
    if (thread.line_type === "file") return false;
    const lineNumber = reviewThreadTargetSide(thread) === "left"
      ? line.old_num
      : line.new_num;
    return lineNumber != null && lineNumber === reviewThreadTargetLine(thread);
  }

  function hasRenderedReviewThread(thread: ReviewThread): boolean {
    if (file.is_binary) return false;
    return file.hunks.some((hunk) =>
      hunk.lines.some((line) => lineMatchesReviewThread(line, thread)),
    );
  }

  const fileLevelReviewThreads = $derived(
    fileReviewThreads.filter((thread) => !hasRenderedReviewThread(thread)),
  );

  function lineRef(
    line: DiffFileType["hunks"][number]["lines"][number],
    side: ReviewSide,
    order: number,
    hunkIndex: number,
  ): ReviewLineRef | null {
    const lineNumber = side === "right" ? line.new_num : line.old_num;
    if (lineNumber == null) return null;
    return {
      side,
      order,
      hunkIndex,
      line: lineNumber,
      oldLine: line.old_num,
      newLine: line.new_num,
      lineType: line.type,
    };
  }

  function selectableLines(side: ReviewSide): ReviewLineRef[] {
    const refs: ReviewLineRef[] = [];
    let order = 0;
    for (let hunkIndex = 0; hunkIndex < file.hunks.length; hunkIndex++) {
      const hunk = file.hunks[hunkIndex]!;
      for (const line of hunk.lines) {
        const ref = lineRef(line, side, order, hunkIndex);
        if (ref) refs.push(ref);
        order += 1;
      }
    }
    return refs;
  }

  function pierreSide(side: ReviewSide): PierreSide {
    return side === "left" ? "deletions" : "additions";
  }

  function reviewSide(side: PierreSide | undefined): ReviewSide {
    return side === "deletions" ? "left" : "right";
  }

  function refForSelection(line: number, side: ReviewSide): ReviewLineRef | null {
    return selectableLineRefs[side].find((ref) => ref.line === line) ?? null;
  }

  function rangeFor(start: ReviewLineRef, end: ReviewLineRef): DiffReviewLineRange {
    const [first, last] = start.order <= end.order ? [start, end] : [end, start];
    return {
      path: file.path,
      side: last.side,
      line: last.line,
      line_type: last.lineType,
      ...(file.old_path !== file.path && { old_path: file.old_path }),
      ...(first.order !== last.order && {
        start_side: first.side,
        start_line: first.line,
      }),
      ...(last.oldLine != null && { old_line: last.oldLine }),
      ...(last.newLine != null && { new_line: last.newLine }),
      ...(diffHeadSHA && { diff_head_sha: diffHeadSHA }),
    };
  }

  function selectedLinesFor(start: ReviewLineRef, end: ReviewLineRef): SelectedLineRange {
    return {
      start: start.line,
      end: end.line,
      side: pierreSide(start.side),
      ...(start.side !== end.side && { endSide: pierreSide(end.side) }),
    };
  }

  function normalizedSelection(
    selection: SelectedLineRange,
  ): { selected: SelectedLineRange; range: DiffReviewLineRange } | null {
    if (!reviewEnabled || !diffHeadSHA) return null;
    const startSide = reviewSide(selection.side);
    const endSide = reviewSide(selection.endSide ?? selection.side);
    const start = refForSelection(selection.start, startSide);
    const end = refForSelection(selection.end, endSide);
    if (!start || !end) return null;
    if (
      !nativeMultilineRanges ||
      start.side !== end.side ||
      start.hunkIndex !== end.hunkIndex
    ) {
      return {
        selected: selectedLinesFor(end, end),
        range: rangeFor(end, end),
      };
    }
    return {
      selected: selectedLinesFor(start, end),
      range: rangeFor(start, end),
    };
  }

  function handlePierreSelection(selection: SelectedLineRange | null): void {
    if (!selection) {
      closeComposer();
      return;
    }
    const normalized = normalizedSelection(selection);
    if (!normalized) {
      closeComposer();
      return;
    }
    selectedRange = normalized.selected;
    composerRange = normalized.range;
  }

  function commentSide(comment: DiffReviewDraftComment): ReviewSide {
    return comment.side.toLowerCase() === "left" ? "left" : "right";
  }

  function renderAnnotation(annotation: DiffLineAnnotation<DiffAnnotation>): HTMLElement {
    const target = document.createElement("div");
    target.className = "pierre-annotation-host";
    const context = new Map([[STORES_KEY, stores]]);
    const metadata = annotation.metadata;
    const component = metadata.kind === "draft"
      ? mount(DiffReviewDraftInlineComment, {
        target,
        props: { comment: metadata.comment },
        context,
      })
      : metadata.kind === "thread"
        ? mount(DiffReviewThreadInlineComment, {
          target,
          props: { thread: metadata.thread },
          context,
        })
        : mount(DiffInlineCommentComposer, {
          target,
          props: { range: metadata.range, onclose: closeComposer },
          context,
        });
    observeUnmount(target, component);
    return target;
  }

  function observeUnmount(target: HTMLElement, component: object): void {
    if (typeof MutationObserver === "undefined") return;
    const observer = new MutationObserver(() => {
      if (target.isConnected) return;
      observer.disconnect();
      void unmount(component);
    });
    observer.observe(document, { childList: true, subtree: true });
  }

  function closeComposer(): void {
    composerRange = null;
    selectedRange = null;
  }

  let reviewContextKey = "";
  $effect(() => {
    const nextKey = reviewEnabled && diffHeadSHA
      ? `${file.path}:${file.old_path ?? ""}:${diffHeadSHA}`
      : "";
    if (nextKey !== reviewContextKey) {
      reviewContextKey = nextKey;
      composerRange = null;
      selectedRange = null;
    }
  });

  function hasCollapsedContext(f: DiffFileType): boolean {
    let previousOldEnd = 1;
    for (const hunk of f.hunks) {
      if (hunk.old_start > previousOldEnd) return true;
      previousOldEnd = hunk.old_start + hunk.old_count;
    }
    return false;
  }
</script>

<div class="diff-file" data-file-path={file.path} bind:this={fileEl}>
  <button class="file-header" onclick={toggle} title={collapsed ? "Expand file" : "Collapse file"}>
    <svg class="collapse-chevron" class:collapse-chevron--collapsed={collapsed} width="12" height="12" viewBox="0 0 12 12" fill="none">
      <path d="M3 4.5L6 7.5L9 4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
    </svg>
    <span class="file-path" class:file-path--deleted={file.status === "deleted"}>
      {displayPath(file)}
    </span>
    <span class="file-stats">
      <DiffStats
        additions={file.additions}
        deletions={file.deletions}
        dimZeros
      />
    </span>
  </button>
  {#if !collapsed}
    <div class="file-content">
      {#each fileLevelReviewThreads as thread (thread.id)}
        <DiffReviewThreadInlineComment {thread} fileLevel={true} />
      {/each}
      {#if showRichPreview}
        {#key richPreviewKey}
          <DiffRichPreview
            {file}
            {provider}
            {platformHost}
            {owner}
            {name}
            {repoPath}
            {number}
            active={inViewport}
          />
        {/key}
      {:else if file.is_binary}
        <div class="binary-notice">Binary file changed</div>
      {:else}
        {#if canLoadMoreContext}
          <button class="context-loader" onclick={() => { void loadFullContext(); }}>
            Load more context
          </button>
        {/if}
        <diffs-container class="pierre-diff" bind:this={pierreDiffEl}></diffs-container>
        {#if contextError}
          <div class="context-error">Could not load more context: {contextError}</div>
        {/if}
      {/if}
    </div>
  {/if}
</div>

<style>
  .diff-file {
    border-top: 2px solid var(--diff-border);
  }

  .file-header {
    position: sticky;
    top: 0;
    z-index: 2;
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 12px;
    background: var(--diff-header-bg);
    border-bottom: 1px solid var(--diff-border);
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
    color: var(--diff-text);
  }

  .file-header:hover {
    background: var(--bg-surface-hover);
  }

  .collapse-chevron {
    transition: transform 0.15s ease-out;
    flex-shrink: 0;
  }

  .collapse-chevron--collapsed {
    transform: rotate(-90deg);
  }

  .file-path {
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    color: var(--diff-text);
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .file-path--deleted {
    text-decoration: line-through;
  }

  .file-stats {
    display: flex;
    flex-shrink: 0;
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .file-content {
    overflow-x: auto;
    container-type: inline-size;
    background: var(--diff-bg);
  }

  :global(.diff-area--word-wrap) .file-content {
    overflow-x: hidden;
  }

  .pierre-diff {
    min-width: 100%;
    width: 100%;
  }

  .context-loader {
    width: 100%;
    padding: 5px 12px;
    color: var(--text-secondary);
    background: var(--diff-header-bg);
    border-bottom: 1px solid var(--diff-border);
    font-size: var(--font-size-xs);
    text-align: left;
    cursor: pointer;
  }

  .context-loader:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .context-error {
    padding: 6px 12px;
    color: var(--danger);
    border-top: 1px solid var(--diff-border);
    font-size: var(--font-size-xs);
  }

  .binary-notice {
    padding: 20px;
    text-align: center;
    color: var(--diff-line-num);
    font-size: var(--font-size-md);
    font-style: italic;
  }

  .pierre-diff:empty {
    min-height: 48px;
  }
</style>
