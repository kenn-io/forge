<script lang="ts">
  import { FileDiff } from "@pierre/diffs";
  import type {
    DiffLineAnnotation,
    ExpansionDirections,
    FileContents,
    FileDiffMetadata,
    FileDiffOptions,
    GetLineIndexUtility,
    SelectedLineRange,
    ThemeTypes,
  } from "@pierre/diffs";
  import { onMount } from "svelte";
  import type { DiffFile } from "../../api/types.js";
  import { appThemeType, diffFileWithPatch, parsePierreFileDiff } from "./pierre-diff.js";
  import { getPierreDiffWorkerPool } from "./pierre-worker-pool.js";

  interface Props {
    file: DiffFile | null | undefined;
    active?: boolean;
    wordWrap?: boolean;
    tabWidth?: number;
    loadFileText?: ((side: "old" | "new") => Promise<string>) | undefined;
    lineAnnotations?: DiffLineAnnotation<unknown>[];
    transientLineAnnotation?: DiffLineAnnotation<unknown> | null;
    selectedRange?: SelectedLineRange | null;
    selectedRanges?: SelectedLineRange[];
    enableLineSelection?: boolean;
    onLineSelected?: (selection: SelectedLineRange | null) => void;
    renderAnnotation?: (annotation: DiffLineAnnotation<unknown>) => HTMLElement | undefined;
  }

  type PierreSide = NonNullable<Parameters<GetLineIndexUtility>[1]>;
  type RenderedLinePair = {
    content: HTMLElement;
    gutter: HTMLElement;
  };
  type TransientAnnotationRow = {
    content?: HTMLElement;
    gutter?: HTMLElement;
    key: string;
    wrapper: HTMLElement;
  };
  const emptyFile: DiffFile = {
    path: "",
    old_path: "",
    status: "modified",
    is_binary: false,
    is_whitespace_only: false,
    additions: 0,
    deletions: 0,
    patch: "",
    hunks: [],
  };

  const {
    file,
    active = true,
    wordWrap = false,
    tabWidth = 4,
    loadFileText,
    lineAnnotations = [],
    transientLineAnnotation = null,
    selectedRange = null,
    selectedRanges = [],
    enableLineSelection = false,
    onLineSelected,
    renderAnnotation,
  }: Props = $props();

  let host: HTMLDivElement | undefined = $state();
  let pierreDiff: FileDiff<unknown> | undefined;
  let demandContextHandlerRoot: ShadowRoot | undefined;
  let fullContext: { oldFile: FileContents; newFile: FileContents } | undefined = $state();
  let contextLoadPromise: Promise<{ oldFile: FileContents; newFile: FileContents }> | undefined;
  let contextError: string | null = $state(null);
  let themeType = $state<ThemeTypes>(appThemeType());
  let rendered = $state(false);
  let placeholderHeight = $state(0);
  let renderedFileKey = "";
  let renderAttemptKey = "";
  let inactiveCleanupTimer: ReturnType<typeof setTimeout> | undefined;
  let reviewRangeFrame: number | undefined;
  let renderRetryFrame: number | undefined;
  let renderRetryTick = $state(0);
  let viewportProbeFrame: number | undefined;
  let viewportProbeTick = $state(0);
  let renderRetryCount = 0;
  let renderedLineRows = new Map<number, RenderedLinePair[]>();
  let selectedRangeElements = new Set<HTMLElement>();
  let transientAnnotationRow: TransientAnnotationRow | undefined;
  const inactiveCleanupDelayMs = 10_000;
  const maxImmediateRenderRetries = 5;

  const renderFile = $derived(file ? diffFileWithPatch(file) : emptyFile);
  const fileKey = $derived(`${renderFile.path}\0${renderFile.old_path}\0${renderFile.patch}`);
  const fileHunks = $derived(renderFile.hunks ?? []);
  const emptyTextualDiff = $derived(!renderFile.patch.trim() || fileHunks.length === 0);
  const pierreFile = $derived.by<FileDiffMetadata | undefined>(() => {
    return parsePierreFileDiff(renderFile, {
      // Pierre marks patch-only diffs as partial and hides expansion controls.
      // Give it sparse line arrays so the controls render; the first click is
      // intercepted, full contents are fetched, and the same expansion replays.
      enableDemandContextExpansion: Boolean(loadFileText) && hasCollapsedContext(renderFile),
    });
  });

  const pierreOptions = $derived.by<FileDiffOptions<unknown>>(() => ({
    diffStyle: "unified",
    diffIndicators: "bars",
    disableFileHeader: true,
    enableLineSelection,
    hunkSeparators: "line-info",
    lineDiffType: "word",
    lineHoverHighlight: enableLineSelection ? "both" : "disabled",
    ...(onLineSelected && {
      onLineSelected,
    }),
    ...(renderAnnotation && { renderAnnotation }),
    overflow: wordWrap ? "wrap" : "scroll",
    theme: { dark: "pierre-dark", light: "pierre-light" },
    themeType,
    expansionLineCount: 40,
    tokenizeMaxLineLength: 2_000,
    onPostRender: () => {
      applyLineTargetAttributes();
      applyHunkHeaderLabels();
      applyLineCommentButtons();
      rendered = true;
      if (!fullContext) {
        installDemandContextHandler();
      }
      scheduleSelectedRangesApplication();
    },
    unsafeCSS: `
      :host {
        display: block;
        font-family: var(--font-mono);
        --diffs-font-family: var(--font-mono);
        --diffs-tab-size: ${tabWidth};
        --diffs-light-bg: var(--bg-surface, #fff);
        --diffs-dark-bg: var(--bg-surface, #16161e);
        --diffs-addition-color-override: var(--accent-green);
        --diffs-deletion-color-override: var(--accent-red);
        --diffs-bg-addition-override: light-dark(
          color-mix(in srgb, var(--accent-green) 12%, transparent),
          color-mix(in srgb, var(--accent-green) 38%, black)
        );
        --diffs-bg-deletion-override: light-dark(
          color-mix(in srgb, var(--accent-red) 14%, transparent),
          color-mix(in srgb, var(--accent-red) 54%, black)
        );
        --diffs-fg-number-addition-override: var(--accent-green);
        --diffs-bg-addition-number-override: var(--accent-green);
        --diffs-fg-number-deletion-override: var(--accent-red);
        --diffs-bg-deletion-number-override: var(--accent-red);
        --diffs-bg-addition-emphasis-override: light-dark(
          color-mix(in srgb, var(--accent-green) 22%, transparent),
          color-mix(
            in srgb,
            transparent 76%,
            color-mix(in srgb, var(--accent-green) 42%, black)
          )
        );
        --diffs-bg-deletion-emphasis-override: light-dark(
          color-mix(in srgb, var(--accent-red) 24%, transparent),
          color-mix(
            in srgb,
            transparent 76%,
            color-mix(in srgb, var(--accent-red) 58%, black)
          )
        );
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
      [data-middleman-line-comment-cell] {
        position: relative;
      }
      [data-middleman-line-comment-cell] > [data-line-number-content] {
        pointer-events: none;
      }
      [data-middleman-line-comment-button] {
        position: absolute;
        top: 50%;
        right: 2px;
        z-index: 1;
        display: grid;
        place-items: center;
        width: 18px;
        height: 18px;
        padding: 0;
        transform: translateY(-50%);
        border: 1px solid var(--border-muted);
        border-radius: 4px;
        background: var(--bg-surface);
        color: var(--text-secondary);
        cursor: pointer;
        font: inherit;
        line-height: 1;
        opacity: 0;
      }
      [data-line-type]:hover > [data-middleman-line-comment-button],
      [data-middleman-line-comment-button]:focus-visible {
        opacity: 1;
      }
      [data-middleman-line-comment-button]::before {
        content: "+";
      }
    `,
  }));

  onMount(() => {
    let themeObserver: MutationObserver | undefined;
    if (typeof MutationObserver !== "undefined") {
      themeObserver = new MutationObserver(() => {
        themeType = appThemeType();
      });
      themeObserver.observe(document.documentElement, {
        attributeFilter: ["class"],
      });
    }

    return () => {
      themeObserver?.disconnect();
      cancelInactiveCleanup();
      cancelSelectedRangesApplication();
      cancelRenderRetry();
      cancelViewportProbe();
      cleanUpPierreDiff();
      contextLoadPromise = undefined;
    };
  });

  $effect(() => {
    if (renderedFileKey === fileKey) return;
    renderedFileKey = fileKey;
    cancelInactiveCleanup();
    cleanUpPierreDiff();
    contextLoadPromise = undefined;
    contextError = null;
    fullContext = undefined;
    rendered = false;
    placeholderHeight = 0;
    renderAttemptKey = "";
    renderRetryCount = 0;
    cancelRenderRetry();
  });

  $effect(() => {
    if (!host || typeof ResizeObserver === "undefined") return;
    const resizeObserver = new ResizeObserver(() => {
      if (rendered || emptyTextualDiff) return;
      renderRetryCount = 0;
      renderRetryTick += 1;
    });
    resizeObserver.observe(host);
    return () => resizeObserver.disconnect();
  });

  $effect(() => {
    const currentViewportProbeTick = viewportProbeTick;
    const currentRenderRetryTick = renderRetryTick;
    if (currentRenderRetryTick < 0 || currentViewportProbeTick < 0) return;
    if (!active && !isHostNearViewport()) {
      scheduleInactiveCleanup();
      return;
    }
    cancelInactiveCleanup();
    if (emptyTextualDiff) {
      cleanUpPierreDiff();
      renderAttemptKey = "";
      rendered = true;
      placeholderHeight = 0;
      return;
    }
    if (!host) return;
    if (!pierreFile) return;
    pierreDiff ??= new FileDiff<unknown>(pierreOptions, getPierreDiffWorkerPool());
    pierreDiff.setOptions(pierreOptions);
    const nextRenderAttemptKey = [
      fileKey,
      wordWrap,
      tabWidth,
      fullContext ? "full" : "patch",
      enableLineSelection,
      annotationKey(lineAnnotations),
    ].join("\0");
    if (renderAttemptKey === nextRenderAttemptKey) {
      pierreDiff.setSelectedLines(selectedRange);
      scheduleSelectedRangesApplication();
      return;
    }
    rendered = false;
    clearRenderedDomState();
    if (fullContext) {
      if (renderFullContext(fullContext)) {
        renderAttemptKey = nextRenderAttemptKey;
        renderRetryCount = 0;
      } else {
        scheduleRenderRetry();
      }
    } else {
      const didRender = pierreDiff.render({
        fileContainer: host,
        fileDiff: pierreFile,
        forceRender: true,
        lineAnnotations,
      });
      if (didRender) {
        renderAttemptKey = nextRenderAttemptKey;
        renderRetryCount = 0;
        applyLineTargetAttributes();
        applyHunkHeaderLabels();
        applyLineCommentButtons();
        rendered = true;
        placeholderHeight = 0;
        installDemandContextHandler();
        scheduleSelectedRangesApplication();
      } else {
        scheduleRenderRetry();
      }
    }
    pierreDiff.setSelectedLines(selectedRange);
    scheduleSelectedRangesApplication();
  });

  $effect(() => {
    if (!host || rendered) return;
    const root = host.closest(".diff-area");
    if (!(root instanceof HTMLElement)) return;
    root.addEventListener("scroll", scheduleViewportProbe, { passive: true });
    window.addEventListener("resize", scheduleViewportProbe);
    scheduleViewportProbe();
    return () => {
      root.removeEventListener("scroll", scheduleViewportProbe);
      window.removeEventListener("resize", scheduleViewportProbe);
      cancelViewportProbe();
    };
  });

  $effect(() => {
    if (active && pierreDiff && pierreFile) {
      pierreDiff.setThemeType(themeType);
    }
  });

  $effect(() => {
    pierreDiff?.setSelectedLines(selectedRange);
    scheduleSelectedRangesApplication();
  });

  $effect(() => {
    const rangeKey = selectedRangesKey(selectedRanges);
    if (rangeKey || selectedRanges.length === 0) {
      scheduleSelectedRangesApplication();
    }
  });

  $effect(() => {
    const transientAnnotationKey = transientLineAnnotation
      ? stableAnnotationKey(transientLineAnnotation)
      : "";
    if (!transientAnnotationKey && !transientAnnotationRow) return;
    if (!rendered || !host?.shadowRoot) return;
    cancelSelectedRangesApplication();
    applyTransientLineAnnotation();
    applySelectedRanges();
  });

  function installDemandContextHandler(): void {
    const root = host?.shadowRoot;
    if (!root || root === demandContextHandlerRoot) return;
    removeDemandContextHandler();
    demandContextHandlerRoot = root;
    root.addEventListener("click", handleDemandContextClick, { capture: true });
  }

  function removeDemandContextHandler(): void {
    demandContextHandlerRoot?.removeEventListener("click", handleDemandContextClick, {
      capture: true,
    });
    demandContextHandlerRoot = undefined;
  }

  function cleanUpPierreDiff(): void {
    removeDemandContextHandler();
    cancelSelectedRangesApplication();
    cancelRenderRetry();
    clearSelectedRangeElements();
    clearTransientLineAnnotation();
    renderedLineRows = new Map();
    pierreDiff?.cleanUp();
    pierreDiff = undefined;
  }

  function cancelRenderRetry(): void {
    if (renderRetryFrame == null) return;
    cancelAnimationFrame(renderRetryFrame);
    renderRetryFrame = undefined;
  }

  function scheduleViewportProbe(): void {
    if (viewportProbeFrame != null) return;
    viewportProbeFrame = requestAnimationFrame(() => {
      viewportProbeFrame = undefined;
      viewportProbeTick += 1;
    });
  }

  function cancelViewportProbe(): void {
    if (viewportProbeFrame == null) return;
    cancelAnimationFrame(viewportProbeFrame);
    viewportProbeFrame = undefined;
  }

  function scheduleRenderRetry(): void {
    if (renderRetryFrame != null || renderRetryCount >= maxImmediateRenderRetries) return;
    renderRetryCount += 1;
    renderRetryFrame = requestAnimationFrame(() => {
      renderRetryFrame = undefined;
      renderRetryTick += 1;
    });
  }

  function cancelSelectedRangesApplication(): void {
    if (reviewRangeFrame == null) return;
    cancelAnimationFrame(reviewRangeFrame);
    reviewRangeFrame = undefined;
  }

  function scheduleSelectedRangesApplication(): void {
    if (!rendered || !host?.shadowRoot) return;
    cancelSelectedRangesApplication();
    reviewRangeFrame = requestAnimationFrame(() => {
      reviewRangeFrame = undefined;
      applyTransientLineAnnotation();
      applySelectedRanges();
    });
  }

  function applySelectedRanges(): void {
    const root = host?.shadowRoot;
    const pre = root?.querySelector("pre");
    if (!root || !pre) return;
    clearSelectedRangeElements();
    const ranges = selectedRange ? [selectedRange, ...selectedRanges] : selectedRanges;
    if (!ranges.length || !pierreDiff) return;

    const split = pre.getAttribute("data-diff-type") === "split";
    const getLineIndex = getPierreLineIndex(pierreDiff);
    for (const range of ranges) {
      const startIndexes = getLineIndex(range.start, range.side as PierreSide);
      const endIndexes = getLineIndex(
        range.end,
        (range.endSide ?? range.side) as PierreSide,
      );
      if (!startIndexes || !endIndexes) continue;
      const startIndex = split ? startIndexes[1] : startIndexes[0];
      const endIndex = split ? endIndexes[1] : endIndexes[0];
      markSelectedLineIndexes(
        Math.min(startIndex, endIndex),
        Math.max(startIndex, endIndex),
        range.side as PierreSide,
      );
    }
  }

  function markSelectedLineIndexes(
    first: number,
    last: number,
    side: PierreSide,
  ): void {
    const isSingle = first === last;
    for (let lineIndex = first; lineIndex <= last; lineIndex += 1) {
      for (const { content: contentElement, gutter: gutterElement } of renderedLineRows.get(lineIndex) ?? []) {
        let value = isSingle ? "single" : lineIndex === first ? "first" : lineIndex === last ? "last" : "";
        markSelectedRangeElement(contentElement, value);
        markSelectedRangeElement(gutterElement, value, side);
        if (
          contentElement.nextSibling instanceof HTMLElement &&
          gutterElement.nextSibling instanceof HTMLElement &&
          contentElement.nextSibling.hasAttribute("data-line-annotation")
        ) {
          if (isSingle) {
            value = "last";
            contentElement.setAttribute("data-selected-line", "first");
          } else if (lineIndex === first || lineIndex === last) {
            value = "";
          }
          markSelectedRangeElement(contentElement.nextSibling, value);
          markSelectedRangeElement(gutterElement.nextSibling, value, side);
        }
      }
    }
  }

  function markSelectedRangeElement(
    element: HTMLElement,
    value: string,
    side?: PierreSide,
  ): void {
    selectedRangeElements.add(element);
    element.setAttribute("data-review-range-line", "");
    element.setAttribute("data-selected-line", value);
    if (side && element.hasAttribute("data-column-number")) {
      element.classList.add("gutter--selected", side === "deletions" ? "gutter-old" : "gutter-new");
    }
  }

  function clearSelectedRangeElements(): void {
    for (const element of selectedRangeElements) {
      element.removeAttribute("data-review-range-line");
      element.removeAttribute("data-selected-line");
      element.classList.remove("gutter--selected", "gutter-new", "gutter-old");
    }
    selectedRangeElements.clear();
  }

  function clearRenderedDomState(): void {
    clearSelectedRangeElements();
    clearTransientLineAnnotation();
    renderedLineRows = new Map();
  }

  function parseRenderedLineIndex(element: HTMLElement, split: boolean): number | undefined {
    const indexes = (element.getAttribute("data-line-index") ?? "")
      .split(",")
      .map((value) => Number.parseInt(value, 10))
      .filter((value) => !Number.isNaN(value));
    if (split && indexes.length === 2) return indexes[1];
    if (!split) return indexes[0];
    return undefined;
  }

  function scheduleInactiveCleanup(): void {
    if (!pierreDiff || inactiveCleanupTimer) return;
    inactiveCleanupTimer = setTimeout(() => {
      inactiveCleanupTimer = undefined;
      if (active || !pierreDiff) return;
      placeholderHeight = measuredRenderedHeight();
      cleanUpPierreDiff();
      renderAttemptKey = "";
      rendered = false;
    }, inactiveCleanupDelayMs);
  }

  function cancelInactiveCleanup(): void {
    if (!inactiveCleanupTimer) return;
    clearTimeout(inactiveCleanupTimer);
    inactiveCleanupTimer = undefined;
  }

  function isHostNearViewport(): boolean {
    if (!host) return false;
    const root = host.closest(".diff-area");
    if (!(root instanceof HTMLElement)) return false;
    const rootRect = root.getBoundingClientRect();
    const hostRect = host.getBoundingClientRect();
    return hostRect.bottom > rootRect.top - 600 &&
      hostRect.top < rootRect.bottom + 600;
  }

  function measuredRenderedHeight(): number {
    const height = host?.getBoundingClientRect().height ?? 0;
    return Number.isFinite(height) && height > 0 ? Math.ceil(height) : placeholderHeight;
  }

  function handleDemandContextClick(event: Event): void {
    if (fullContext) return;
    const target = closestFromEvent(event, "[data-expand-button], [data-unmodified-lines]");
    if (!target) return;
    const separator = target.closest("[data-separator][data-expand-index]");
    const hunkIndex = Number(separator?.getAttribute("data-expand-index"));
    if (!Number.isFinite(hunkIndex)) return;

    event.preventDefault();
    event.stopImmediatePropagation();
    const expandAll = isExpandAllClick(target, event);
    const direction = expandAll ? "both" : expansionDirection(target);
    const expansionLineCount = expandAll ? Number.POSITIVE_INFINITY : undefined;
    void loadFullContextAndExpand(hunkIndex, direction, expansionLineCount)
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

  function isExpandAllClick(target: Element, event: Event): boolean {
    return target.hasAttribute("data-expand-all-button")
      || (event instanceof MouseEvent && event.shiftKey);
  }

  async function loadFullContextAndExpand(
    hunkIndex: number,
    direction: ExpansionDirections,
    expansionLineCount: number | undefined,
  ): Promise<void> {
    const requestFileKey = fileKey;
    const context = await loadFullContext(requestFileKey);
    if (!context || fileKey !== requestFileKey) return;
    renderFullContext(context);
    if (fileKey !== requestFileKey) return;
    clearRenderedDomState();
    pierreDiff?.expandHunk(hunkIndex, direction, expansionLineCount);
    applyLineTargetAttributes();
    applyHunkHeaderLabels();
    applyLineCommentButtons();
    scheduleSelectedRangesApplication();
  }

  function renderFullContext(context: { oldFile: FileContents; newFile: FileContents }): boolean {
    if (!pierreDiff || !host) return false;
    rendered = false;
    clearRenderedDomState();
    const didRender = pierreDiff.render({
      fileContainer: host,
      oldFile: context.oldFile,
      newFile: context.newFile,
      forceRender: true,
      lineAnnotations,
    });
    pierreDiff.setSelectedLines(selectedRange);
    if (didRender) {
      applyLineTargetAttributes();
      applyHunkHeaderLabels();
      applyLineCommentButtons();
      rendered = true;
      placeholderHeight = 0;
      scheduleSelectedRangesApplication();
    }
    return didRender;
  }

  async function loadFullContext(
    requestFileKey: string,
  ): Promise<{ oldFile: FileContents; newFile: FileContents } | undefined> {
    if (fullContext) return fullContext;
    const promise = contextLoadPromise ??= fetchFullContext();
    try {
      const context = await promise;
      if (fileKey !== requestFileKey || contextLoadPromise !== promise) return undefined;
      fullContext = context;
    } catch (err) {
      if (contextLoadPromise === promise) {
        contextLoadPromise = undefined;
      }
      if (fileKey !== requestFileKey) return undefined;
      throw err;
    }
    return fullContext;
  }

  async function fetchFullContext(): Promise<{ oldFile: FileContents; newFile: FileContents }> {
    if (!loadFileText) {
      throw new Error("Context loading is unavailable");
    }
    contextError = null;
    const [oldContents, newContents] = await Promise.all([
      renderFile.status === "added" ? Promise.resolve("") : loadFileText("old"),
      renderFile.status === "deleted" ? Promise.resolve("") : loadFileText("new"),
    ]);
    return {
      oldFile: {
        name: renderFile.old_path || renderFile.path,
        contents: oldContents,
      },
      newFile: {
        name: renderFile.path,
        contents: newContents,
      },
    };
  }

  function annotationKey(annotations: DiffLineAnnotation<unknown>[]): string {
    return annotations.map((annotation) => {
      const metadata = annotation.metadata as { id?: unknown } | undefined;
      return [
        annotation.side,
        annotation.lineNumber,
        String(metadata?.id ?? ""),
        stableAnnotationKey(metadata),
      ].join(":");
    }).join("|");
  }

  function stableAnnotationKey(value: unknown): string {
    const seen = new WeakSet<object>();
    return JSON.stringify(value, (_key, current: unknown) => {
      if (!current || typeof current !== "object") return current;
      if (seen.has(current)) return "[Circular]";
      seen.add(current);
      if (Array.isArray(current)) return current;
      return Object.keys(current)
        .sort()
        .reduce<Record<string, unknown>>((sorted, key) => {
          sorted[key] = (current as Record<string, unknown>)[key];
          return sorted;
        }, {});
    }) ?? "";
  }

  function selectedRangesKey(ranges: SelectedLineRange[]): string {
    return ranges.map((range) =>
      `${range.side}:${range.start}:${range.endSide ?? range.side}:${range.end}`
    ).join("|");
  }

  function getPierreLineIndex(diff: FileDiff<unknown>): GetLineIndexUtility {
    return diff.getLineIndex;
  }

  function applyLineTargetAttributes(): void {
    const root = host?.shadowRoot;
    const pre = root?.querySelector("pre");
    if (!root || !pre || !pierreDiff) return;
    for (const line of root.querySelectorAll<HTMLElement>("[data-diff-path]")) {
      line.removeAttribute("data-diff-path");
      line.removeAttribute("data-diff-old-line");
      line.removeAttribute("data-diff-new-line");
    }

    const split = pre.getAttribute("data-diff-type") === "split";
    refreshRenderedLineRows(pre, split);
    const getLineIndex = getPierreLineIndex(pierreDiff);
    for (const hunk of fileHunks) {
      for (const line of hunk.lines) {
        if (line.old_num != null) {
          markLineTarget(pre, getLineIndex(line.old_num, "deletions"), split, {
            "data-diff-old-line": String(line.old_num),
          });
        }
        if (line.new_num != null) {
          markLineTarget(pre, getLineIndex(line.new_num, "additions"), split, {
            "data-diff-new-line": String(line.new_num),
          });
        }
      }
    }
  }

  function applyLineCommentButtons(): void {
    const root = host?.shadowRoot;
    const pre = root?.querySelector("pre");
    if (!root || !pre) return;
    for (const button of root.querySelectorAll("[data-middleman-line-comment-button]")) {
      button.remove();
    }
    for (const cell of root.querySelectorAll("[data-middleman-line-comment-cell]")) {
      cell.removeAttribute("data-middleman-line-comment-cell");
    }
    if (!enableLineSelection || !onLineSelected) return;

    for (const code of Array.from(pre.children)) {
      const [gutter, content] = Array.from(code.children);
      if (!gutter || !content) continue;
      const contentRows = Array.from(content.children);
      const gutterRows = Array.from(gutter.children);
      for (let index = 0; index < contentRows.length; index += 1) {
        const contentElement = contentRows[index];
        const gutterElement = gutterRows[index];
        if (!(contentElement instanceof HTMLElement) || !(gutterElement instanceof HTMLElement)) {
          continue;
        }
        const target = lineCommentTarget(contentElement);
        if (!target) continue;
        gutterElement.setAttribute("data-middleman-line-comment-cell", "");
        gutterElement.appendChild(lineCommentButton(target));
      }
    }
  }

  function lineCommentTarget(
    element: HTMLElement,
  ): { lineNumber: number; side: PierreSide } | undefined {
    const newLine = parseLineAttribute(element, "data-diff-new-line");
    if (newLine != null) return { lineNumber: newLine, side: "additions" };
    const oldLine = parseLineAttribute(element, "data-diff-old-line");
    if (oldLine != null) return { lineNumber: oldLine, side: "deletions" };
    return undefined;
  }

  function parseLineAttribute(element: HTMLElement, name: string): number | undefined {
    const value = Number.parseInt(element.getAttribute(name) ?? "", 10);
    return Number.isFinite(value) ? value : undefined;
  }

  function lineCommentButton(target: { lineNumber: number; side: PierreSide }): HTMLButtonElement {
    const button = document.createElement("button");
    const sideLabel = target.side === "additions" ? "new" : "old";
    const label = `Comment on ${sideLabel} line ${target.lineNumber}`;
    button.type = "button";
    button.title = label;
    button.setAttribute("aria-label", label);
    button.setAttribute("data-middleman-line-comment-button", "");
    button.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      onLineSelected?.(lineCommentSelection(target, event));
    });
    return button;
  }

  function lineCommentSelection(
    target: { lineNumber: number; side: PierreSide },
    event: MouseEvent,
  ): SelectedLineRange {
    if (event.shiftKey && selectedRange) {
      return {
        start: selectedRange.start,
        side: selectedRange.side ?? target.side,
        end: target.lineNumber,
        endSide: target.side,
      };
    }
    return {
      start: target.lineNumber,
      side: target.side,
      end: target.lineNumber,
    };
  }

  function applyTransientLineAnnotation(): void {
    const annotation = transientLineAnnotation;
    if (!annotation || !host || !renderAnnotation) {
      clearTransientLineAnnotation();
      return;
    }

    const slotName = annotationSlotName(annotation);
    const key = [
      slotName,
      stableAnnotationKey(annotation.metadata),
    ].join(":");
    if (transientAnnotationRow?.key === key) return;

    clearTransientLineAnnotation();

    const existingSlot = hasAnnotationSlot(slotName);
    const row = existingSlot ? undefined : insertTransientAnnotationRow(annotation);
    if (!existingSlot && !row) return;

    const content = renderAnnotation(annotation);
    if (!content) return;

    const wrapper = document.createElement("div");
    wrapper.dataset.transientAnnotationSlot = "";
    wrapper.slot = slotName;
    wrapper.style.whiteSpace = "normal";
    wrapper.appendChild(content);

    // eslint-disable-next-line svelte/no-dom-manipulating -- Pierre owns this custom element; annotations are passed through its light-DOM slot API.
    host.appendChild(wrapper);
    transientAnnotationRow = {
      key,
      wrapper,
      ...row,
    };
  }

  function clearTransientLineAnnotation(): void {
    transientAnnotationRow?.wrapper.remove();
    transientAnnotationRow?.content?.remove();
    transientAnnotationRow?.gutter?.remove();
    transientAnnotationRow = undefined;
  }

  function hasAnnotationSlot(slotName: string): boolean {
    const root = host?.shadowRoot;
    if (!root) return false;
    for (const slot of root.querySelectorAll<HTMLSlotElement>("slot")) {
      if (slot.name === slotName) return true;
    }
    return false;
  }

  function insertTransientAnnotationRow(
    annotation: DiffLineAnnotation<unknown>,
  ): { content: HTMLElement; gutter: HTMLElement } | undefined {
    const root = host?.shadowRoot;
    const pre = root?.querySelector("pre");
    if (!pre || !pierreDiff) return undefined;

    const split = pre.getAttribute("data-diff-type") === "split";
    const indexes = getPierreLineIndex(pierreDiff)(
      annotation.lineNumber,
      annotation.side as PierreSide,
    );
    if (!indexes) return undefined;

    const lineIndex = split ? indexes[1] : indexes[0];
    const target = renderedLinePair(pre, lineIndex, split);
    if (!target) return undefined;

    const gutter = document.createElement("div");
    gutter.setAttribute("data-gutter-buffer", "annotation");
    gutter.setAttribute("data-buffer-size", "1");
    gutter.style.gridRow = "span 1";

    const content = document.createElement("div");
    content.setAttribute("data-line-annotation", `0,${lineIndex}`);
    const annotationContent = document.createElement("div");
    annotationContent.setAttribute("data-annotation-content", "");
    const slot = document.createElement("slot");
    slot.name = annotationSlotName(annotation);
    annotationContent.appendChild(slot);
    content.appendChild(annotationContent);

    target.gutter.after(gutter);
    target.content.after(content);
    return { content, gutter };
  }

  function annotationSlotName(annotation: DiffLineAnnotation<unknown>): string {
    return `annotation-${annotation.side}-${annotation.lineNumber}`;
  }

  function markLineTarget(
    pre: HTMLPreElement,
    indexes: [number, number] | undefined,
    split: boolean,
    attributes: Record<string, string>,
  ): void {
    if (!indexes) return;
    const lineIndex = split ? indexes[1] : indexes[0];
    if (!Number.isFinite(lineIndex)) return;
    const pair = renderedLinePair(pre, lineIndex, split);
    if (!pair) return;
    pair.content.setAttribute("data-diff-path", renderFile.path);
    pair.content.tabIndex = -1;
    for (const [name, value] of Object.entries(attributes)) {
      pair.content.setAttribute(name, value);
    }
  }

  function refreshRenderedLineRows(pre: HTMLPreElement, split: boolean): void {
    const next = new Map<number, RenderedLinePair[]>();
    for (const code of Array.from(pre.children)) {
      const [gutter, content] = Array.from(code.children);
      if (!gutter || !content) continue;
      const contentRows = Array.from(content.children);
      const gutterRows = Array.from(gutter.children);
      for (let index = 0; index < contentRows.length; index += 1) {
        const contentElement = contentRows[index];
        const gutterElement = gutterRows[index];
        if (!(contentElement instanceof HTMLElement) || !(gutterElement instanceof HTMLElement)) {
          continue;
        }
        const lineIndex = parseRenderedLineIndex(contentElement, split);
        if (lineIndex == null) continue;
        const rows = next.get(lineIndex) ?? [];
        rows.push({ content: contentElement, gutter: gutterElement });
        next.set(lineIndex, rows);
      }
    }
    renderedLineRows = next;
  }

  function renderedLinePair(
    pre: HTMLPreElement,
    targetIndex: number,
    split: boolean,
  ): RenderedLinePair | undefined {
    const cached = renderedLineRows.get(targetIndex)?.[0];
    if (cached) return cached;
    for (const code of Array.from(pre.children)) {
      const [gutter, content] = Array.from(code.children);
      if (!gutter || !content) continue;
      const gutterRows = Array.from(gutter.children);
      for (const contentElement of Array.from(content.children)) {
        if (!(contentElement instanceof HTMLElement)) continue;
        const lineIndex = parseRenderedLineIndex(contentElement, split);
        if (lineIndex === targetIndex) {
          const index = Array.prototype.indexOf.call(content.children, contentElement);
          const gutterElement = gutterRows[index];
          if (gutterElement instanceof HTMLElement) {
            return { content: contentElement, gutter: gutterElement };
          }
        }
        if ((lineIndex ?? 0) > targetIndex) return undefined;
      }
    }
    return undefined;
  }

  function hasCollapsedContext(f: DiffFile): boolean {
    let previousOldEnd = 1;
    for (const hunk of f.hunks ?? []) {
      if (hunk.old_start > previousOldEnd) return true;
      previousOldEnd = hunk.old_start + hunk.old_count;
    }
    return false;
  }

  function applyHunkHeaderLabels(): void {
    const root = host?.shadowRoot;
    if (!root || !pierreFile) return;

    const labels = root.querySelectorAll<HTMLElement>(
      "[data-separator='line-info'] [data-unmodified-lines]",
    );
    let nextSeparatorHunkIndex = 0;
    for (const label of labels) {
      const separator = label.closest("[data-separator][data-expand-index]");
      let hunkIndex = Number(separator?.getAttribute("data-expand-index"));
      if (!Number.isFinite(hunkIndex)) {
        hunkIndex = nextRenderedSeparatorHunkIndex(pierreFile, nextSeparatorHunkIndex);
      }
      if (Number.isFinite(hunkIndex)) {
        nextSeparatorHunkIndex = Math.max(nextSeparatorHunkIndex, hunkIndex + 1);
      }
      const hunkHeader = Number.isFinite(hunkIndex)
        ? pierreFile.hunks[hunkIndex]?.hunkSpecs?.trim()
        : undefined;
      if (!hunkHeader) continue;

      const lineInfo = label.textContent?.trim() ?? "";
      if (lineInfo.startsWith(`${hunkHeader} - `)) continue;
      label.textContent = lineInfo && lineInfo !== hunkHeader
        ? `${hunkHeader} - ${lineInfo}`
        : hunkHeader;
    }
  }

  function nextRenderedSeparatorHunkIndex(
    fileDiff: FileDiffMetadata,
    startIndex: number,
  ): number {
    let hunkIndex = startIndex;
    while (hunkIndex < fileDiff.hunks.length) {
      if ((fileDiff.hunks[hunkIndex]?.collapsedBefore ?? 0) > 0) return hunkIndex;
      hunkIndex += 1;
    }
    return Number.NaN;
  }
</script>

<div
  class="pierre-diff-shell"
  class:pierre-diff-shell--loading={!rendered}
  style:min-height={placeholderHeight ? `${placeholderHeight}px` : undefined}
  aria-busy={!rendered}
>
  {#if !emptyTextualDiff}
    <diffs-container
      class="pierre-diff"
      class:pierre-diff--pending={!rendered}
      bind:this={host}
    ></diffs-container>
  {/if}
  {#if rendered && emptyTextualDiff}
    <div class="empty-textual-diff">No textual changes</div>
  {/if}
  {#if !rendered}
    <div class="pierre-diff-loading" role="status" aria-live="polite">
      <svg class="pierre-diff-spinner" width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
        <circle cx="10" cy="10" r="8" stroke="currentColor" stroke-opacity="0.2" stroke-width="2" />
        <path d="M18 10a8 8 0 0 0-8-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
      </svg>
      <span>Loading diff</span>
    </div>
  {/if}
</div>
{#if contextError}
  <div class="context-error">Could not load more context: {contextError}</div>
{/if}

<style>
  .pierre-diff-shell {
    position: relative;
    min-width: 100%;
    width: 100%;
    background: var(--bg-surface);
  }

  .pierre-diff-shell--loading {
    min-height: 96px;
  }

  .pierre-diff {
    min-width: 100%;
    width: 100%;
  }

  .pierre-diff--pending {
    visibility: hidden;
  }

  .pierre-diff:empty {
    min-height: 48px;
  }

  .empty-textual-diff {
    padding: 20px;
    color: var(--diff-line-num);
    font-size: var(--font-size-sm);
    font-style: italic;
    text-align: center;
  }

  .pierre-diff-loading {
    position: absolute;
    inset: 0;
    display: flex;
    min-height: 96px;
    align-items: center;
    justify-content: center;
    gap: 8px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    background: var(--bg-surface);
  }

  .pierre-diff-spinner {
    animation: spin 0.8s linear infinite;
  }

  .context-error {
    padding: 6px 12px;
    color: var(--danger);
    border-top: 1px solid var(--diff-border);
    font-size: var(--font-size-xs);
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
