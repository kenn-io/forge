<script lang="ts">
  import type { DiffLineAnnotation, SelectedLineRange, Virtualizer } from "@pierre/diffs";
  import { mount, onMount, unmount } from "svelte";
  import type { DiffFile as DiffFileType } from "../../api/types.js";
  import type { DiffReviewDraftComment } from "../../stores/diff-review-draft.svelte.js";
  import type { DiffReviewLineRange } from "../../stores/diff-review-draft.svelte.js";
  import { STORES_KEY, getStores } from "../../context.js";
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
  import PierreFileDiff from "./PierreFileDiff.svelte";

  const stores = getStores();
  const diffStore = stores.diff;
  const diffReviewDraft = stores.diffReviewDraft;

  interface Props {
    file: DiffFileType;
    provider: string;
    platformHost?: string | undefined;
    owner: string;
    name: string;
    repoPath: string;
    number: number;
    richPreviewEnabled?: boolean;
    contextExpansionEnabled?: boolean;
    reviewEnabled?: boolean;
    canReplyToThreads?: boolean;
    diffHeadSHA?: string | undefined;
    nativeMultilineRanges?: boolean;
    reviewThreads?: ReviewThread[];
    virtualizer?: Virtualizer | undefined;
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
    contextExpansionEnabled = true,
    reviewEnabled = false,
    canReplyToThreads = false,
    diffHeadSHA = undefined,
    nativeMultilineRanges = false,
    reviewThreads = [],
    virtualizer,
  }: Props = $props();

  const collapsed = $derived(diffStore.isFileCollapsed(owner, name, number, file.path));
  const richPreview = $derived(diffStore.getRichPreview());
  const wordWrap = $derived(diffStore.getWordWrap());
  const viewMode = $derived(diffStore.getViewMode());
  const tabWidth = $derived(diffStore.getTabWidth());
  const filePreviewGeneration = $derived(diffStore.getFilePreviewGeneration());
  const showRichPreview = $derived(
    richPreviewEnabled && richPreview && supportsRichPreview(file.path),
  );
  const richPreviewKey = $derived(`${file.path}:${filePreviewGeneration}`);
  const textDiffKey = $derived(`${file.path}:${file.old_path ?? ""}:${filePreviewGeneration}`);
  const fileDraftComments = $derived(
    diffReviewDraft.getComments().filter((comment) => comment.path === file.path),
  );
  const fileReviewThreads = $derived(
    reviewThreads.filter((thread) => threadMatchesFile(thread)),
  );
  const fileHunks = $derived(file.hunks ?? []);

  // Track viewport visibility so off-screen files skip expensive tokenization
  // on whitespace toggles and theme switches. Starts false so the initial
  // render on large diffs doesn't eagerly tokenize every file before the
  // IntersectionObserver reports visibility — the first observer callback
  // fires synchronously for on-screen files.
  let fileEl: HTMLDivElement | undefined = $state();
  let inViewport = $state(false);
  type MountedAnnotation = {
    component: object;
    observer?: MutationObserver;
    target: HTMLElement;
  };
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
    | { kind: "thread"; id: string; thread: ReviewThread; canReply: boolean }
    | { kind: "composer"; id: string; range: DiffReviewLineRange };
  const mountedAnnotations = new Set<MountedAnnotation>();

  let selectedRange = $state<SelectedLineRange | null>(null);
  let composerRange = $state<DiffReviewLineRange | null>(null);
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
        metadata: { kind: "thread", id: thread.id, thread, canReply: canReplyToThreads },
      });
    }
    return annotations;
  });
  const pierreLineAnnotations = $derived(lineAnnotations as DiffLineAnnotation<unknown>[]);
  const composerAnnotation = $derived.by<DiffLineAnnotation<DiffAnnotation> | null>(() => {
    if (!reviewEnabled || !composerRange) return null;
    return {
      side: pierreSide(reviewSideFromValue(composerRange.side)),
      lineNumber: composerRange.line,
      metadata: {
        kind: "composer",
        id: `composer:${rangeKey(composerRange)}`,
        range: composerRange,
      },
    };
  });
  const pierreComposerAnnotation = $derived(
    composerAnnotation as DiffLineAnnotation<unknown> | null,
  );
  const draftSelectedRanges = $derived.by<SelectedLineRange[]>(() => {
    if (!reviewEnabled) return [];
    const ranges: SelectedLineRange[] = [];
    for (const comment of fileDraftComments) {
      const range = selectedRangeForDraftComment(comment);
      if (range) ranges.push(range);
    }
    return ranges;
  });

  onMount(() => {
    let observer: IntersectionObserver | undefined;
    // Guard for jsdom / SSR-ish test environments where IntersectionObserver
    // is not provided — treat the file as visible so rendering still runs.
    if (typeof IntersectionObserver === "undefined") {
      inViewport = true;
    } else if (fileEl) {
      const root = fileEl.closest(".diff-area");
      observer = new IntersectionObserver(
        (entries) => { inViewport = entries[0]!.isIntersecting; },
        { root, rootMargin: "600px 0px" },
      );
      observer.observe(fileEl);
    }

    return () => {
      observer?.disconnect();
      clearMountedAnnotations();
    };
  });

  function toggle(): void {
    diffStore.toggleFileCollapsed(owner, name, number, file.path);
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
    return fileHunks.some((hunk) =>
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
    for (let hunkIndex = 0; hunkIndex < fileHunks.length; hunkIndex++) {
      const hunk = fileHunks[hunkIndex]!;
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

  function rangeKey(range: DiffReviewLineRange): string {
    return [
      range.start_side ?? range.side,
      range.start_line ?? range.line,
      range.side,
      range.line,
    ].join(":");
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
    return reviewSideFromValue(comment.side);
  }

  function selectedRangeForDraftComment(comment: DiffReviewDraftComment): SelectedLineRange | null {
    if (comment.line_type === "file") return null;
    if (!commentMatchesCurrentDiff(comment)) return null;
    const endSide = commentSide(comment);
    const end = refForSelection(comment.line, endSide);
    if (!end) return null;
    const startLine = comment.start_line ?? comment.line;
    const startSide = comment.start_side ? reviewSideFromValue(comment.start_side) : endSide;
    const start = refForSelection(startLine, startSide);
    if (!start || start.hunkIndex !== end.hunkIndex || start.side !== end.side) {
      return selectedLinesFor(end, end);
    }
    return selectedLinesFor(start, end);
  }

  function commentMatchesCurrentDiff(comment: DiffReviewDraftComment): boolean {
    return !comment.diff_head_sha || !diffHeadSHA || comment.diff_head_sha === diffHeadSHA;
  }

  function reviewSideFromValue(side: string): ReviewSide {
    return side.toLowerCase() === "left" ? "left" : "right";
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
          props: {
            thread: metadata.thread,
            canReply: metadata.canReply,
            onreply: replyToThread,
          },
          context,
        })
        : mount(DiffInlineCommentComposer, {
          target,
          props: { range: metadata.range, onclose: closeComposer },
          context,
        });
    trackMountedAnnotation(target, component);
    return target;
  }

  function renderUnknownAnnotation(annotation: DiffLineAnnotation<unknown>): HTMLElement {
    return renderAnnotation(annotation as DiffLineAnnotation<DiffAnnotation>);
  }

  function trackMountedAnnotation(target: HTMLElement, component: object): void {
    const mounted: MountedAnnotation = { component, target };
    mountedAnnotations.add(mounted);
    const cleanUp = () => {
      if (!mountedAnnotations.delete(mounted)) return;
      mounted.observer?.disconnect();
      void unmount(component);
    };
    if (typeof MutationObserver === "undefined") return;
    mounted.observer = new MutationObserver(() => {
      if (!target.isConnected) cleanUp();
    });
    queueMicrotask(() => {
      if (!mountedAnnotations.has(mounted)) return;
      if (!target.isConnected) {
        cleanUp();
        return;
      }
      const root = target.getRootNode();
      const observedRoot = root instanceof ShadowRoot || root instanceof Document
        ? root
        : document;
      mounted.observer?.observe(observedRoot, { childList: true, subtree: true });
    });
  }

  function clearMountedAnnotations(): void {
    for (const mounted of mountedAnnotations) {
      mountedAnnotations.delete(mounted);
      mounted.observer?.disconnect();
      void unmount(mounted.component);
    }
  }

  async function replyToThread(thread: ReviewThread, body: string): Promise<boolean> {
    return await stores.detail?.replyToDiscussion(owner, name, number, thread.id, body) ?? false;
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
        {#key textDiffKey}
          <PierreFileDiff
            {file}
            active={inViewport}
            {viewMode}
            {wordWrap}
            {tabWidth}
            loadFileText={contextExpansionEnabled ? loadDiffText : undefined}
            lineAnnotations={pierreLineAnnotations}
            transientLineAnnotation={pierreComposerAnnotation}
            selectedRange={selectedRange}
            selectedRanges={draftSelectedRanges}
            enableLineSelection={reviewEnabled && !!diffHeadSHA}
            onLineSelected={handlePierreSelection}
            renderAnnotation={renderUnknownAnnotation}
            {virtualizer}
          />
        {/key}
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

  .binary-notice {
    padding: 20px;
    text-align: center;
    color: var(--diff-line-num);
    font-size: var(--font-size-md);
    font-style: italic;
  }

</style>
