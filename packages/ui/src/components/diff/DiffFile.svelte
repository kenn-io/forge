<script lang="ts">
  import { onMount } from "svelte";
  import type { DiffFile as DiffFileType, DiffHunk } from "../../api/types.js";
  import { getStores } from "../../context.js";

  const stores = getStores();
  const diffStore = stores.diff;
  const diffReviewDraft = stores.diffReviewDraft;
  import { tokenizeLineDual, langFromPath, type DualToken } from "../../utils/highlight.js";
  import type {
    DiffReviewDraftComment,
    DiffReviewLineRange,
  } from "../../stores/diff-review-draft.svelte.js";
  import DiffLineComponent from "./DiffLine.svelte";
  import DiffInlineCommentComposer from "./DiffInlineCommentComposer.svelte";
  import DiffReviewDraftInlineComment from "./DiffReviewDraftInlineComment.svelte";
  import DiffReviewThreadInlineComment from "./DiffReviewThreadInlineComment.svelte";
  import CollapsedRegion from "./CollapsedRegion.svelte";
  import DiffRichPreview from "./DiffRichPreview.svelte";
  import DiffStats from "../shared/DiffStats.svelte";
  import {
    reviewThreadTargetLine,
    reviewThreadTargetSide,
    type ReviewThread,
  } from "./review-thread-context.js";

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
  const lang = $derived(langFromPath(file.path));
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
  let inViewport = $state(false);

  // Local copy of file data, only synced when expanded AND visible. Collapsed
  // or off-screen files keep stale content so whitespace toggles and theme
  // switches don't trigger expensive re-renders and re-tokenization for
  // content no one can see.
  // svelte-ignore state_referenced_locally — synced from file prop via $effect
  let renderedFile = $state(file);
  const lineNumberGutterWidth = $derived(
    `calc(${lineNumberDigitCount(renderedFile.hunks) + 1}ch + 10px)`,
  );

  $effect(() => {
    if (!collapsed && inViewport) {
      const prev = renderedFile;
      renderedFile = file;
      // Clear stale tokens synchronously so any render before the
      // tokenization effect runs falls through to raw content
      // instead of showing cached tokens from the old file.
      if (file !== prev) {
        tokens = new Map();
      }
    }
  });

  onMount(() => {
    let observer: IntersectionObserver | undefined;
    // Guard for jsdom / SSR-ish test environments where IntersectionObserver
    // is not provided — treat the file as visible so tokenization still runs.
    if (typeof IntersectionObserver === "undefined") {
      inViewport = true;
      return;
    }
    if (fileEl) {
      observer = new IntersectionObserver(
        (entries) => { inViewport = entries[0]!.isIntersecting; },
        { rootMargin: "200px 0px" },
      );
      observer.observe(fileEl);
    }

    return () => { observer?.disconnect(); };
  });

  // Dual-theme token cache — each span carries both colors as CSS custom
  // properties, so theme switch is pure CSS (zero DOM updates, zero
  // re-renders). Tokenization happens once per line using Shiki's native
  // dual-theme API, which guarantees aligned token boundaries across themes.
  let tokens = $state<Map<string, DualToken[]>>(new Map());
  let tokenVersion = 0;

  // Plain (non-reactive) tracking of the last tokenized source and whether
  // tokenization finished. Used to distinguish source changes (which need a
  // fresh cache) from visibility flips (which should reuse the cache).
  let lastSourceFile: DiffFileType | undefined;
  let lastSourceLang: string | undefined;
  let tokenizationComplete = false;

  // Tokenize in small batches to avoid blocking the main thread.
  const BATCH_SIZE = 50;

  // Tokenize for BOTH themes when file data changes.
  // Skipped for collapsed or off-screen files; runs when they become visible.
  // Does NOT depend on `theme` — theme switches just swap which cache is read.
  $effect(() => {
    const version = ++tokenVersion;
    const currentFile = renderedFile;
    const currentLang = lang;
    const sourceChanged =
      currentFile !== lastSourceFile || currentLang !== lastSourceLang;

    if (sourceChanged) {
      lastSourceFile = currentFile;
      lastSourceLang = currentLang;
      tokenizationComplete = false;
    }

    if (collapsed || !inViewport) return;
    // Already fully tokenized for this source — scrolling back into view or
    // re-expanding should reuse the cached tokens, not rebuild them.
    if (tokenizationComplete) return;

    // About to (re)start tokenization for this source — clear any stale or
    // partial entries so the first batch doesn't render a mix of old and
    // new keys while the async tokenization walks the hunks.
    tokens = new Map();
    const next = new Map<string, DualToken[]>();

    void (async () => {
      const items: Array<{ key: string; content: string }> = [];
      for (let hi = 0; hi < currentFile.hunks.length; hi++) {
        const hunk = currentFile.hunks[hi]!;
        for (let li = 0; li < hunk.lines.length; li++) {
          items.push({ key: `${hi}:${li}`, content: hunk.lines[li]!.content });
        }
      }

      for (let i = 0; i < items.length; i += BATCH_SIZE) {
        if (version !== tokenVersion) return;
        const batch = items.slice(i, i + BATCH_SIZE);
        const results = await Promise.all(
          batch.map(async (item) => ({
            key: item.key,
            spans: await tokenizeLineDual(item.content, currentLang),
          })),
        );
        if (version !== tokenVersion) return;
        for (const r of results) {
          next.set(r.key, r.spans);
        }
        // Update reactively after each batch so lines get highlighted progressively.
        tokens = new Map(next);
        // Yield to the browser between batches.
        if (i + BATCH_SIZE < items.length) {
          await new Promise((r) => requestAnimationFrame(r));
        }
      }
      if (version === tokenVersion) {
        tokenizationComplete = true;
      }
    })();
  });

  function getTokens(hunkIdx: number, lineIdx: number): DualToken[] {
    const key = `${hunkIdx}:${lineIdx}`;
    const cached = tokens.get(key);
    if (cached) return cached;
    return [{ content: renderedFile.hunks[hunkIdx]!.lines[lineIdx]!.content }];
  }

  function computeCollapsedLines(hunks: DiffHunk[], hunkIdx: number): number {
    if (hunkIdx === 0) return 0;
    const prev = hunks[hunkIdx - 1]!;
    const curr = hunks[hunkIdx]!;
    const prevEndOld = prev.old_start + prev.old_count;
    const gapOld = curr.old_start - prevEndOld;
    return Math.max(gapOld, 0);
  }

  function lineNumberDigitCount(hunks: DiffHunk[]): number {
    let maxLine = 1;

    for (const hunk of hunks) {
      if (hunk.old_count > 0) {
        maxLine = Math.max(maxLine, hunk.old_start + hunk.old_count - 1);
      }
      if (hunk.new_count > 0) {
        maxLine = Math.max(maxLine, hunk.new_start + hunk.new_count - 1);
      }
      for (const line of hunk.lines) {
        if (line.old_num != null) maxLine = Math.max(maxLine, line.old_num);
        if (line.new_num != null) maxLine = Math.max(maxLine, line.new_num);
      }
    }

    return String(maxLine).length;
  }

  function toggle(): void {
    diffStore.toggleFileCollapsed(owner, name, number, file.path);
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

  function reviewThreadsAfter(
    line: DiffFileType["hunks"][number]["lines"][number],
  ): ReviewThread[] {
    return fileReviewThreads.filter((thread) => lineMatchesReviewThread(line, thread));
  }

  function hasRenderedReviewThread(thread: ReviewThread): boolean {
    if (renderedFile.is_binary) return false;
    return renderedFile.hunks.some((hunk) =>
      hunk.lines.some((line) => lineMatchesReviewThread(line, thread)),
    );
  }

  const fileLevelReviewThreads = $derived(
    fileReviewThreads.filter((thread) => !hasRenderedReviewThread(thread)),
  );

  type ReviewSide = "left" | "right";
  type ReviewLineRef = {
    side: ReviewSide;
    order: number;
    hunkIndex: number;
    line: number;
    oldLine?: number | undefined;
    newLine?: number | undefined;
    lineType: "context" | "add" | "delete";
  };

  let selectionAnchor = $state<ReviewLineRef | null>(null);
  let selectedRange = $state<{ start: ReviewLineRef; end: ReviewLineRef } | null>(null);
  let composerRange = $state<DiffReviewLineRange | null>(null);
  const selectableLineRefs = $derived.by(() => ({
    left: selectableLines("left"),
    right: selectableLines("right"),
  }));

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
    for (let hunkIndex = 0; hunkIndex < renderedFile.hunks.length; hunkIndex++) {
      const hunk = renderedFile.hunks[hunkIndex]!;
      for (const line of hunk.lines) {
        const ref = lineRef(line, side, order, hunkIndex);
        if (ref) refs.push(ref);
        order += 1;
      }
    }
    return refs;
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

  function handleLineSelect(
    line: DiffFileType["hunks"][number]["lines"][number],
    side: ReviewSide,
    order: number,
    hunkIndex: number,
    event: MouseEvent,
  ): void {
    if (!reviewEnabled || !diffHeadSHA) return;
    const current = lineRef(line, side, order, hunkIndex);
    if (!current) return;
    const anchor = selectionAnchor;
    if (
      nativeMultilineRanges &&
      event.shiftKey &&
      anchor?.side === side &&
      anchor.hunkIndex === current.hunkIndex
    ) {
      const refs = selectableLineRefs[side];
      const anchorIndex = refs.findIndex((ref) => ref.order === anchor.order);
      const currentIndex = refs.findIndex((ref) => ref.order === current.order);
      if (anchorIndex !== -1 && currentIndex !== -1) {
        const [startIndex, endIndex] = anchorIndex <= currentIndex
          ? [anchorIndex, currentIndex]
          : [currentIndex, anchorIndex];
        const start = refs[startIndex]!;
        const end = refs[endIndex]!;
        selectedRange = { start, end };
        composerRange = rangeFor(start, end);
        return;
      }
    }
    selectionAnchor = current;
    selectedRange = { start: current, end: current };
    composerRange = rangeFor(current, current);
  }

  function isSelected(
    line: DiffFileType["hunks"][number]["lines"][number],
    side: ReviewSide,
    order: number,
    hunkIndex: number,
  ): boolean {
    if (!selectedRange) return false;
    const current = lineRef(line, side, order, hunkIndex);
    if (!current || current.side !== selectedRange.start.side) return false;
    if (current.hunkIndex !== selectedRange.start.hunkIndex) return false;
    const min = Math.min(selectedRange.start.order, selectedRange.end.order);
    const max = Math.max(selectedRange.start.order, selectedRange.end.order);
    return current.order >= min && current.order <= max;
  }

  function composerAfter(
    line: DiffFileType["hunks"][number]["lines"][number],
    order: number,
    hunkIndex: number,
  ): boolean {
    if (!composerRange || !selectedRange) return false;
    const max = Math.max(selectedRange.start.order, selectedRange.end.order);
    return order === max && lineRef(line, selectedRange.end.side, order, hunkIndex) !== null;
  }

  function commentSide(comment: DiffReviewDraftComment): ReviewSide {
    return comment.side.toLowerCase() === "left" ? "left" : "right";
  }

  function commentStartSide(comment: DiffReviewDraftComment): ReviewSide {
    return comment.start_side?.toLowerCase() === "left"
      ? "left"
      : commentSide(comment);
  }

  function draftCommentsAfter(
    line: DiffFileType["hunks"][number]["lines"][number],
    order: number,
    hunkIndex: number,
  ): DiffReviewDraftComment[] {
    return fileDraftComments.filter((comment) => {
      const side = commentSide(comment);
      const ref = lineRef(line, side, order, hunkIndex);
      return ref?.line === comment.line;
    });
  }

  function isInDraftRange(
    line: DiffFileType["hunks"][number]["lines"][number],
    side: ReviewSide,
    order: number,
    hunkIndex: number,
  ): boolean {
    const current = lineRef(line, side, order, hunkIndex);
    if (!current) return false;
    return fileDraftComments.some((comment) => {
      const endSide = commentSide(comment);
      const startSide = commentStartSide(comment);
      if (current.side !== endSide || startSide !== endSide) return false;
      const endRefs = selectableLineRefs[endSide];
      const endRef = endRefs.find((ref) => ref.line === comment.line);
      if (!endRef || current.hunkIndex !== endRef.hunkIndex) return false;
      const startLine = comment.start_line ?? comment.line;
      const startRef = selectableLineRefs[startSide].find((ref) => ref.line === startLine);
      if (!startRef || startRef.hunkIndex !== endRef.hunkIndex) return false;
      const min = Math.min(startRef.order, endRef.order);
      const max = Math.max(startRef.order, endRef.order);
      return current.order >= min && current.order <= max;
    });
  }

  function closeComposer(): void {
    composerRange = null;
    selectedRange = null;
    selectionAnchor = null;
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
      selectionAnchor = null;
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
      {:else if renderedFile.is_binary}
        <div class="binary-notice">Binary file changed</div>
      {:else}
        <div
          class="file-rows"
          style:--diff-line-number-gutter-width={lineNumberGutterWidth}
        >
          {#each renderedFile.hunks as hunk, hunkIdx (`${hunk.old_start}:${hunk.new_start}:${hunkIdx}`)}
            {#if hunkIdx > 0}
              {@const gap = computeCollapsedLines(renderedFile.hunks, hunkIdx)}
              {#if gap > 0}
                <CollapsedRegion lineCount={gap} />
              {/if}
            {/if}
            <div class="hunk-header">
              <span class="hunk-gutter"></span>
              <span class="hunk-text">@@ -{hunk.old_start},{hunk.old_count} +{hunk.new_start},{hunk.new_count} @@{hunk.section ? ` ${hunk.section}` : ""}</span>
            </div>
            {#each hunk.lines as line, lineIdx (`${hunkIdx}:${line.old_num ?? ""}:${line.new_num ?? ""}:${lineIdx}`)}
              {@const order = renderedFile.hunks.slice(0, hunkIdx).reduce((sum, item) => sum + item.lines.length, 0) + lineIdx}
              <div
                class="diff-line-anchor"
                tabindex="-1"
                data-diff-path={file.path}
                {...(line.old_num != null ? { "data-diff-old-line": String(line.old_num) } : {})}
                {...(line.new_num != null ? { "data-diff-new-line": String(line.new_num) } : {})}
              >
                <DiffLineComponent
                  type={line.type}
                  content={line.content}
                  {...(line.old_num != null ? { oldNum: line.old_num } : {})}
                  {...(line.new_num != null ? { newNum: line.new_num } : {})}
                  {...(line.no_newline ? { noNewline: line.no_newline } : {})}
                  tokens={getTokens(hunkIdx, lineIdx)}
                  {reviewEnabled}
                  oldSelected={isSelected(line, "left", order, hunkIdx) || isInDraftRange(line, "left", order, hunkIdx)}
                  newSelected={isSelected(line, "right", order, hunkIdx) || isInDraftRange(line, "right", order, hunkIdx)}
                  onselectside={(side, event) => handleLineSelect(line, side, order, hunkIdx, event)}
                />
              </div>
              {#if reviewEnabled}
                {#each draftCommentsAfter(line, order, hunkIdx) as comment (comment.id)}
                  <DiffReviewDraftInlineComment {comment} />
                {/each}
              {/if}
              {#each reviewThreadsAfter(line) as thread (thread.id)}
                <DiffReviewThreadInlineComment {thread} />
              {/each}
              {#if reviewEnabled && composerRange && composerAfter(line, order, hunkIdx)}
                <DiffInlineCommentComposer
                  range={composerRange}
                  onclose={closeComposer}
                />
              {/if}
            {/each}
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .diff-file {
    border-top: 2px solid var(--diff-border);
  }

  .diff-line-anchor:focus {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
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
  }

  :global(.diff-area--word-wrap) .file-content {
    overflow-x: hidden;
  }

  .file-rows {
    min-width: 100%;
    width: max-content;
  }

  :global(.diff-area--word-wrap) .file-rows {
    width: 100%;
  }

  .binary-notice {
    padding: 20px;
    text-align: center;
    color: var(--diff-line-num);
    font-size: var(--font-size-md);
    font-style: italic;
  }

  .hunk-header {
    display: flex;
    align-items: stretch;
    background: var(--diff-hunk-bg);
    color: var(--diff-hunk-text);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    line-height: 20px;
  }

  .hunk-gutter {
    width: var(--diff-line-number-gutter-width, 50px);
    flex-shrink: 0;
    background: var(--diff-hunk-bg);
  }

  .hunk-text {
    padding: 2px 12px;
    white-space: pre;
  }

  :global(.diff-area--word-wrap) .hunk-text {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
</style>
