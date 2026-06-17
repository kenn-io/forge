<script lang="ts">
  import { FileDiff, VirtualizedFileDiff } from "@pierre/diffs";
  import type {
    DiffLineAnnotation,
    ExpansionDirections,
    FileContents,
    FileDiffMetadata,
    FileDiffOptions,
    GetLineIndexUtility,
    SelectedLineRange,
    ThemeTypes,
    Virtualizer,
  } from "@pierre/diffs";
  import { onMount, tick } from "svelte";
  import type { DiffFile } from "../../api/types.js";
  import {
    appThemeType,
    debugPierreDiff,
    diffFileWithPatch,
    parsePierreFileDiff,
    parsePierreFileDiffWithContents,
    pierreDiffDebugEnabled,
    pierreFileContents,
  } from "./pierre-diff.js";
  import {
    renderedCodeColumns,
    renderedCodeSide as renderedPierreCodeSide,
  } from "./pierre-dom.js";
  import { diffTokenizeMaxLineLength, getPierreDiffWorkerPool } from "./pierre-worker-pool.js";

  interface Props {
    file: DiffFile | null | undefined;
    active?: boolean;
    viewMode?: "unified" | "split";
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
    virtualizer?: Virtualizer | undefined;
  }

  type PierreSide = NonNullable<Parameters<GetLineIndexUtility>[1]>;
  type RenderedLinePair = {
    content: HTMLElement;
    gutter: HTMLElement;
    side: PierreSide | undefined;
  };
  type TransientInsertedAnnotationRow = {
    content: HTMLElement;
    gutter: HTMLElement;
  };
  type TransientAnnotationRow = {
    content?: HTMLElement;
    gutter?: HTMLElement;
    insertedRows?: TransientInsertedAnnotationRow[];
    key: string;
    wrapper: HTMLElement;
  };
  type PendingContextExpansion = {
    direction: ExpansionDirections;
    expansionLineCount: number | undefined;
    fileKey: string;
    hunkIndex: number;
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
    file = null,
    active = true,
    viewMode = "unified",
    wordWrap = false,
    tabWidth = 4,
    loadFileText = undefined,
    lineAnnotations = [],
    transientLineAnnotation = null,
    selectedRange = null,
    selectedRanges = [],
    enableLineSelection = false,
    onLineSelected = undefined,
    renderAnnotation = undefined,
    virtualizer = undefined,
  }: Props = $props();

  let host: HTMLElement | undefined = $state();
  let pierreDiff: FileDiff<unknown> | VirtualizedFileDiff<unknown> | undefined;
  let pierreDiffVirtualizer: Virtualizer | undefined;
  let demandContextHandlerRoot: ShadowRoot | undefined;
  let annotationFocusTarget: HTMLElement | undefined;
  let fullContext: { oldFile: FileContents; newFile: FileContents } | undefined = $state();
  let fullContextFileDiff: FileDiffMetadata | undefined;
  let fullContextRendered = false;
  let contextLoadPromise: Promise<{ oldFile: FileContents; newFile: FileContents }> | undefined;
  let contextError: string | null = $state(null);
  let themeType = $state<ThemeTypes>(appThemeType());
  let rendered = $state(false);
  let renderedFileKey = "";
  let renderAttemptKey = "";
  let reviewRangeFrame: number | undefined;
  let renderRetryFrame: number | undefined;
  let renderRetryTick = $state(0);
  let renderRetryCount = 0;
  let renderedLineRows = new Map<number, RenderedLinePair[]>();
  let selectedRangeElements = new Set<HTMLElement>();
  let lineAnnotationWrappers = new Map<string, HTMLElement>();
  let transientAnnotationRow: TransientAnnotationRow | undefined;
  let pendingContextExpansion: PendingContextExpansion | undefined;
  let lineCommentButtonHasPointerSnapshot = false;
  let lineCommentButtonWasSelectedOnPointerDown = false;
  const maxImmediateRenderRetries = 5;

  const renderFile = $derived(file ? diffFileWithPatch(file) : emptyFile);
  const fileKey = $derived(`${renderFile.path}\0${renderFile.old_path}\0${renderFile.patch}`);
  const fileHunks = $derived(renderFile.hunks ?? []);
  const pierreFile = $derived.by<FileDiffMetadata | undefined>(() => {
    return parsePierreFileDiff(renderFile, {
      // Pierre marks patch-only diffs as partial and hides expansion controls.
      // Give it sparse line arrays so the controls render; the first click is
      // intercepted, full contents are fetched, and the same expansion replays.
      enableDemandContextExpansion: Boolean(loadFileText) && hasCollapsedContext(renderFile),
    });
  });
  const hasRenderablePierreDiff = $derived(
    Boolean(
      pierreFile &&
        (pierreFile.hunks.length > 0 || pierreFile.additionLines.length > 0 || pierreFile.deletionLines.length > 0),
    ),
  );
  const emptyTextualDiff = $derived(!renderFile.patch.trim() || !hasRenderablePierreDiff);

  const pierreOptions = $derived.by<FileDiffOptions<unknown>>(() => ({
    diffStyle: viewMode,
    diffIndicators: "bars",
    disableFileHeader: true,
    enableLineSelection: false,
    hunkSeparators: "line-info",
    lineDiffType: "word",
    lineHoverHighlight: "disabled",
    ...(renderAnnotation && { renderAnnotation }),
    overflow: wordWrap ? "wrap" : "scroll",
    theme: { dark: "pierre-dark", light: "pierre-light" },
    themeType,
    expansionLineCount: 40,
    tokenizeMaxLineLength: diffTokenizeMaxLineLength,
    onPostRender: () => {
      removeStalePlaceholderPres();
      applyLineTargetAttributes();
      applyHunkHeaderLabels();
      applyLineCommentButtons();
      syncLineAnnotationWrappers();
      rendered = true;
      installDemandContextHandler();
      scheduleSelectedRangesApplication();
      restoreAnnotationFocus();
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
      code,
      [data-placeholder] {
        contain: none;
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
      cancelSelectedRangesApplication();
      cancelRenderRetry();
      cleanUpPierreDiff();
      contextLoadPromise = undefined;
    };
  });

  $effect(() => {
    const el = host;
    if (!el) return;
    el.addEventListener("focusin", handleAnnotationFocusIn);
    el.addEventListener("focusout", handleAnnotationFocusOut);
    return () => {
      el.removeEventListener("focusin", handleAnnotationFocusIn);
      el.removeEventListener("focusout", handleAnnotationFocusOut);
      annotationFocusTarget = undefined;
    };
  });

  $effect(() => {
    if (renderedFileKey === fileKey && pierreDiffVirtualizer === virtualizer) return;
    renderedFileKey = fileKey;
    pierreDiffVirtualizer = virtualizer;
    cleanUpPierreDiff();
    contextLoadPromise = undefined;
    contextError = null;
    fullContext = undefined;
    fullContextFileDiff = undefined;
    fullContextRendered = false;
    pendingContextExpansion = undefined;
    rendered = emptyTextualDiff;
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
    const currentRenderRetryTick = renderRetryTick;
    if (currentRenderRetryTick < 0) return;
    if (emptyTextualDiff) {
      cleanUpPierreDiff();
      renderAttemptKey = "";
      rendered = true;
      return;
    }
    if (!host) return;
    if (!pierreFile) return;
    if (!active && !virtualizer) return;
    pierreDiff ??= createPierreDiff();
    pierreDiff.setOptions(pierreOptions);
    if (pierreDiff instanceof VirtualizedFileDiff && isHostInScrollViewport()) {
      pierreDiff.setVisibility(true);
    }
    const nextRenderAttemptKey = [
      fileKey,
      viewMode,
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
        removeStalePlaceholderPres();
        applyLineTargetAttributes();
        applyHunkHeaderLabels();
        applyLineCommentButtons();
        syncLineAnnotationWrappers();
        rendered = true;
        installDemandContextHandler();
        scheduleSelectedRangesApplication();
        restoreAnnotationFocus();
      } else {
        scheduleRenderRetry();
      }
    }
    pierreDiff.setSelectedLines(selectedRange);
    scheduleSelectedRangesApplication();
  });

  $effect(() => {
    if (pierreDiff && pierreFile) {
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
    root.addEventListener("slotchange", handleAnnotationSlotChange);
  }

  function handleAnnotationSlotChange(): void {
    // Annotation content becomes focusable again only once a rebuilt shadow
    // DOM re-slots its wrapper; onPostRender runs before that happens.
    restoreAnnotationFocus();
  }

  function annotationWrapperFor(target: EventTarget | null): HTMLElement | undefined {
    if (!(target instanceof HTMLElement) || !host) return undefined;
    let wrapper: HTMLElement | null = target;
    while (wrapper && wrapper.parentElement !== host) wrapper = wrapper.parentElement;
    if (!wrapper) return undefined;
    const isAnnotation = wrapper.dataset.middlemanLineAnnotationWrapper !== undefined ||
      wrapper.dataset.transientAnnotationSlot !== undefined;
    return isAnnotation ? wrapper : undefined;
  }

  function handleAnnotationFocusIn(event: FocusEvent): void {
    const target = event.target;
    annotationFocusTarget = target instanceof HTMLElement && annotationWrapperFor(target)
      ? target
      : undefined;
  }

  function handleAnnotationFocusOut(event: FocusEvent): void {
    // A real focusout means the user (or app) moved focus deliberately.
    // Involuntary focus loss from a re-render unslotting the annotation
    // is browser-dependent: Firefox annuls focus silently (no focusout
    // at all), while Chromium and WebKit fire a focusout with no
    // successor element. Keep the restore target in that involuntary
    // case — identified by the wrapper having lost its slot assignment
    // — so the slotchange handler can reclaim focus once the rebuilt
    // shadow DOM re-slots the annotation.
    if (event.target !== annotationFocusTarget) return;
    if (event.relatedTarget === null) {
      const wrapper = annotationWrapperFor(event.target);
      if (wrapper && wrapper.assignedSlot === null) return;
    }
    annotationFocusTarget = undefined;
  }

  function restoreAnnotationFocus(): void {
    const target = annotationFocusTarget;
    if (!target) return;
    const attempt = (): void => {
      if (annotationFocusTarget !== target) return;
      if (!target.isConnected || host?.contains(target) !== true) return;
      const active = document.activeElement;
      if (active === target) return;
      // Reclaim only focus the browser dropped to the document itself when
      // the diff re-render unslotted the annotation; never steal focus the
      // user has since placed somewhere else.
      if (active !== null && active !== document.body && active !== document.documentElement) {
        return;
      }
      target.focus({ preventScroll: true });
    };
    attempt();
    // Firefox annuls focus for unslotted content asynchronously; re-check
    // once the annulment has landed.
    queueMicrotask(attempt);
  }

  function removeDemandContextHandler(): void {
    demandContextHandlerRoot?.removeEventListener("click", handleDemandContextClick, {
      capture: true,
    });
    demandContextHandlerRoot?.removeEventListener("slotchange", handleAnnotationSlotChange);
    demandContextHandlerRoot = undefined;
  }

  function cleanUpPierreDiff(): void {
    removeDemandContextHandler();
    cancelSelectedRangesApplication();
    cancelRenderRetry();
    clearSelectedRangeElements();
    clearTransientLineAnnotation();
    clearLineAnnotationWrappers();
    renderedLineRows = new Map();
    fullContextFileDiff = undefined;
    pierreDiff?.cleanUp();
    pierreDiff = undefined;
  }

  function createPierreDiff(): FileDiff<unknown> | VirtualizedFileDiff<unknown> {
    const workerPool = getPierreDiffWorkerPool();
    if (!virtualizer) return new FileDiff<unknown>(pierreOptions, workerPool, true);
    return new VirtualizedFileDiff<unknown>(
      pierreOptions,
      virtualizer,
      undefined,
      workerPool,
      true,
    );
  }

  function cancelRenderRetry(): void {
    if (renderRetryFrame == null) return;
    cancelAnimationFrame(renderRetryFrame);
    renderRetryFrame = undefined;
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
    const pre = renderedDiffPre(root);
    if (!root || !pre) return;
    clearSelectedRangeElements();
    if ((!selectedRange && !selectedRanges.length) || !pierreDiff) return;

    const split = pre.getAttribute("data-diff-type") === "split";
    const getLineIndex = getPierreLineIndex(pierreDiff);
    if (selectedRange) {
      markSelectedRange(getLineIndex, split, selectedRange, true);
    }
    for (const range of selectedRanges) {
      markSelectedRange(getLineIndex, split, range, false);
    }
  }

  function markSelectedRange(
    getLineIndex: GetLineIndexUtility,
    split: boolean,
    range: SelectedLineRange,
    active: boolean,
  ): void {
    const startIndexes = getLineIndex(range.start, range.side as PierreSide);
    const endIndexes = getLineIndex(
      range.end,
      (range.endSide ?? range.side) as PierreSide,
    );
    if (!startIndexes || !endIndexes) return;
    const startIndex = split ? startIndexes[1] : startIndexes[0];
    const endIndex = split ? endIndexes[1] : endIndexes[0];
    markSelectedLineIndexes(
      Math.min(startIndex, endIndex),
      Math.max(startIndex, endIndex),
      range.side as PierreSide,
      active,
    );
  }

  function markSelectedLineIndexes(
    first: number,
    last: number,
    side: PierreSide,
    active: boolean,
  ): void {
    const isSingle = first === last;
    for (let lineIndex = first; lineIndex <= last; lineIndex += 1) {
      for (const { content: contentElement, gutter: gutterElement } of renderedLineRows.get(lineIndex) ?? []) {
        let value = isSingle ? "single" : lineIndex === first ? "first" : lineIndex === last ? "last" : "";
        markSelectedRangeElement(contentElement, value, undefined, active);
        markSelectedRangeElement(gutterElement, value, side, active);
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
          markSelectedRangeElement(contentElement.nextSibling, value, undefined, active);
          markSelectedRangeElement(gutterElement.nextSibling, value, side, active);
        }
      }
    }
  }

  function markSelectedRangeElement(
    element: HTMLElement,
    value: string,
    side?: PierreSide,
    active = false,
  ): void {
    selectedRangeElements.add(element);
    element.setAttribute("data-review-range-line", "");
    element.setAttribute("data-selected-line", value);
    if (active) {
      element.setAttribute("data-active-review-range-line", "");
    }
    if (side && element.hasAttribute("data-column-number")) {
      element.classList.add("gutter--selected", side === "deletions" ? "gutter-old" : "gutter-new");
    }
  }

  function clearSelectedRangeElements(): void {
    for (const element of selectedRangeElements) {
      element.removeAttribute("data-review-range-line");
      element.removeAttribute("data-selected-line");
      element.removeAttribute("data-active-review-range-line");
      element.classList.remove("gutter--selected", "gutter-new", "gutter-old");
    }
    selectedRangeElements.clear();
  }

  function clearRenderedDomState(): void {
    clearSelectedRangeElements();
    clearTransientLineAnnotation();
    clearLineAnnotationWrappers();
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

  function handleDemandContextClick(event: Event): void {
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
    debugPierreDiff("expand click intercepted", {
      path: renderFile.path,
      hunkIndex,
      direction,
      expansionLineCount: expansionLineCount ?? "default",
      fullContextLoaded: Boolean(fullContext),
      fullContextRendered,
      sparseCacheKey: pierreFile?.cacheKey,
    });
    void loadFullContextAndExpand(hunkIndex, direction, expansionLineCount)
      .catch((err: unknown) => {
        contextError = err instanceof Error ? err.message : String(err);
        debugPierreDiff("expand failed", {
          path: renderFile.path,
          hunkIndex,
          error: contextError,
        });
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
    const alreadyRendered = fullContextRendered;
    debugPierreDiff("expand loading full context", {
      path: renderFile.path,
      hunkIndex,
      direction,
      alreadyRendered,
    });
    const context = await loadFullContext(requestFileKey);
    if (!context || fileKey !== requestFileKey) return;
    await tick();
    if (fileKey !== requestFileKey) return;
    if (!alreadyRendered && !fullContextRendered) {
      const didRender = renderFullContext(context);
      debugPierreDiff("expand full context render result", {
        path: renderFile.path,
        hunkIndex,
        didRender,
        fullCacheKey: fullContextFileDiff?.cacheKey,
        sparseCacheKey: pierreFile?.cacheKey,
      });
      if (fileKey !== requestFileKey) return;
      if (!didRender) {
        if (!fullContextFileDiff) return;
        pendingContextExpansion = {
          direction,
          expansionLineCount,
          fileKey: requestFileKey,
          hunkIndex,
        };
        scheduleRenderRetry();
        return;
      }
    }
    expandRenderedHunk(hunkIndex, direction, expansionLineCount);
    debugPierreDiff("expand complete", {
      path: renderFile.path,
      hunkIndex,
      rows: pierreDiffDebugEnabled() ? expandedContextDebugRows() : [],
    });
  }

  function expandRenderedHunk(
    hunkIndex: number,
    direction: ExpansionDirections,
    expansionLineCount: number | undefined,
  ): void {
    clearRenderedDomState();
    pierreDiff?.expandHunk(hunkIndex, direction, expansionLineCount);
    if (fullContext && fullContextFileDiff) {
      const didRender = renderFullContextRange(fullContext, fullContextFileDiff);
      debugPierreDiff("expand rerendered full context range", {
        path: renderFile.path,
        hunkIndex,
        didRender,
        fullCacheKey: fullContextFileDiff.cacheKey,
        rows: pierreDiffDebugEnabled() ? expandedContextDebugRows() : [],
      });
      if (!didRender) scheduleRenderRetry();
    }
    removeStalePlaceholderPres();
    applyLineTargetAttributes();
    applyHunkHeaderLabels();
    applyLineCommentButtons();
    syncLineAnnotationWrappers();
    installDemandContextHandler();
    scheduleSelectedRangesApplication();
    restoreAnnotationFocus();
  }

  function renderFullContext(context: { oldFile: FileContents; newFile: FileContents }): boolean {
    if (!pierreDiff || !host) return false;
    fullContextRendered = false;
    rendered = false;
    clearRenderedDomState();
    fullContextFileDiff = parsePierreFileDiffWithContents(renderFile, context) ?? pierreFile;
    debugPierreDiff("render full context prepared", {
      path: renderFile.path,
      sparseCacheKey: pierreFile?.cacheKey,
      fullCacheKey: fullContextFileDiff?.cacheKey,
      oldCacheKey: context.oldFile.cacheKey,
      newCacheKey: context.newFile.cacheKey,
      oldLength: context.oldFile.contents.length,
      newLength: context.newFile.contents.length,
    });
    if (!fullContextFileDiff) return false;
    const didRender = renderFullContextRange(context, fullContextFileDiff);
    pierreDiff.setSelectedLines(selectedRange);
    if (didRender) {
      fullContextRendered = true;
      removeStalePlaceholderPres();
      applyLineTargetAttributes();
      applyHunkHeaderLabels();
      applyLineCommentButtons();
      syncLineAnnotationWrappers();
      rendered = true;
      installDemandContextHandler();
      scheduleSelectedRangesApplication();
      restoreAnnotationFocus();
      replayPendingContextExpansion();
    }
    return didRender;
  }

  function replayPendingContextExpansion(): void {
    const pending = pendingContextExpansion;
    if (!pending || pending.fileKey !== fileKey) return;
    pendingContextExpansion = undefined;
    expandRenderedHunk(
      pending.hunkIndex,
      pending.direction,
      pending.expansionLineCount,
    );
  }

  function renderFullContextRange(
    context: { oldFile: FileContents; newFile: FileContents },
    fileDiff: FileDiffMetadata,
  ): boolean {
    if (!pierreDiff || !host) return false;
    const props = {
      fileContainer: host,
      fileDiff,
      oldFile: context.oldFile,
      newFile: context.newFile,
      forceRender: true,
      lineAnnotations,
    } satisfies Parameters<FileDiff<unknown>["render"]>[0];
    if (!(pierreDiff instanceof VirtualizedFileDiff)) {
      return pierreDiff.render({
        ...props,
        renderRange: {
          startingLine: 0,
          totalLines: Number.POSITIVE_INFINITY,
          bufferBefore: 0,
          bufferAfter: 0,
        },
      });
    }
    if (isHostInScrollViewport()) {
      pierreDiff.setVisibility(true);
    }
    if (pierreDiff.fileDiff !== fileDiff) {
      pierreDiff.fileDiff = fileDiff;
      pierreDiff.setMetrics(undefined, true);
    }
    return pierreDiff.render(props);
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
    debugPierreDiff("fetch full context start", {
      path: renderFile.path,
      status: renderFile.status,
    });
    const [oldContents, newContents] = await Promise.all([
      renderFile.status === "added" ? Promise.resolve("") : loadFileText("old"),
      renderFile.status === "deleted" ? Promise.resolve("") : loadFileText("new"),
    ]);
    const context = {
      oldFile: pierreFileContents(renderFile.old_path || renderFile.path, oldContents, "full-old"),
      newFile: pierreFileContents(renderFile.path, newContents, "full-new"),
    };
    debugPierreDiff("fetch full context complete", {
      path: renderFile.path,
      oldLength: oldContents.length,
      newLength: newContents.length,
      oldCacheKey: context.oldFile.cacheKey,
      newCacheKey: context.newFile.cacheKey,
    });
    return context;
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
    const pre = renderedDiffPre(root);
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
          markLineTarget(
            pre,
            getLineIndex(line.old_num, "deletions"),
            split,
            "deletions",
            {
              "data-diff-old-line": String(line.old_num),
            },
          );
        }
        if (line.new_num != null) {
          markLineTarget(
            pre,
            getLineIndex(line.new_num, "additions"),
            split,
            "additions",
            {
              "data-diff-new-line": String(line.new_num),
            },
          );
        }
      }
    }
  }

  function applyLineCommentButtons(): void {
    const root = host?.shadowRoot;
    const pre = renderedDiffPre(root);
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
    button.addEventListener("pointerdown", (event) => {
      event.stopPropagation();
      lineCommentButtonHasPointerSnapshot = true;
      lineCommentButtonWasSelectedOnPointerDown = lineCommentTargetIsSelected(target, event);
    });
    button.addEventListener("mousedown", (event) => {
      event.stopPropagation();
      lineCommentButtonHasPointerSnapshot = true;
      lineCommentButtonWasSelectedOnPointerDown = lineCommentTargetIsSelected(target, event);
    });
    button.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      const collapse = lineCommentButtonHasPointerSnapshot
        ? lineCommentButtonWasSelectedOnPointerDown
        : lineCommentTargetIsSelected(target, event);
      lineCommentButtonHasPointerSnapshot = false;
      lineCommentButtonWasSelectedOnPointerDown = false;
      onLineSelected?.(
        collapse
          ? null
          : lineCommentSelection(target, event),
      );
    });
    return button;
  }

  function selectedRangeMatchesLineCommentTarget(
    target: { lineNumber: number; side: PierreSide },
    event: MouseEvent,
  ): boolean {
    if (event.shiftKey || !selectedRange) return false;
    const selectedSide = selectedRange.side ?? target.side;
    const selectedEndSide = selectedRange.endSide ?? selectedSide;
    return selectedSide === target.side &&
      selectedEndSide === target.side &&
      selectedRange.start === target.lineNumber &&
      selectedRange.end === target.lineNumber;
  }

  function lineCommentTargetIsSelected(
    target: { lineNumber: number; side: PierreSide },
    event: MouseEvent,
  ): boolean {
    return selectedRangeMatchesLineCommentTarget(target, event) ||
      (!event.shiftKey && selectedLineTargetExists(target));
  }

  function selectedLineTargetExists(target: { lineNumber: number; side: PierreSide }): boolean {
    const attr = target.side === "additions" ? "data-diff-new-line" : "data-diff-old-line";
    return host?.shadowRoot?.querySelector(
      `[data-active-review-range-line][${attr}="${target.lineNumber}"]`,
    ) != null;
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
    if (transientAnnotationRow?.key === key) {
      if (!hasAnnotationSlot(slotName)) {
        const row = insertTransientAnnotationRow(annotation);
        if (row) {
          transientAnnotationRow = {
            ...transientAnnotationRow,
            ...row,
          };
        }
      }
      return;
    }

    clearTransientLineAnnotation();

    const row = hasAnnotationSlot(slotName) ? undefined : insertTransientAnnotationRow(annotation);
    if (!hasAnnotationSlot(slotName) && !row) return;

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
    const row = transientAnnotationRow;
    row?.wrapper.remove();
    const insertedRows = row?.insertedRows ?? (
      row?.content && row.gutter ? [{ content: row.content, gutter: row.gutter }] : []
    );
    const adjustedColumns = new Map<HTMLElement, number>();
    for (const insertedRow of insertedRows) {
      queueColumnSpanAdjustment(adjustedColumns, insertedRow.content.parentElement, -1);
      queueColumnSpanAdjustment(adjustedColumns, insertedRow.gutter.parentElement, -1);
      insertedRow.content.remove();
      insertedRow.gutter.remove();
    }
    applyColumnSpanAdjustments(adjustedColumns);
    transientAnnotationRow = undefined;
  }

  function syncLineAnnotationWrappers(): void {
    if (!host || !renderAnnotation) {
      clearLineAnnotationWrappers();
      return;
    }
    const activeKeys = new Set<string>();
    for (const annotation of lineAnnotations) {
      const slotName = annotationSlotName(annotation);
      const key = `${slotName}:${stableAnnotationKey(annotation)}`;
      activeKeys.add(key);
      if (lineAnnotationWrappers.has(key)) continue;

      const content = renderAnnotation(annotation);
      if (!content) continue;

      const wrapper = document.createElement("div");
      wrapper.dataset.middlemanLineAnnotationWrapper = "";
      wrapper.slot = slotName;
      wrapper.style.whiteSpace = "normal";
      wrapper.appendChild(content);
      // eslint-disable-next-line svelte/no-dom-manipulating -- Pierre owns this custom element; annotations are passed through its light-DOM slot API.
      host.appendChild(wrapper);
      lineAnnotationWrappers.set(key, wrapper);
    }

    for (const [key, wrapper] of lineAnnotationWrappers) {
      if (activeKeys.has(key)) continue;
      lineAnnotationWrappers.delete(key);
      wrapper.remove();
    }
  }

  function clearLineAnnotationWrappers(): void {
    for (const wrapper of lineAnnotationWrappers.values()) {
      wrapper.remove();
    }
    lineAnnotationWrappers.clear();
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
  ): { content: HTMLElement; gutter: HTMLElement; insertedRows: TransientInsertedAnnotationRow[] } | undefined {
    const root = host?.shadowRoot;
    const pre = renderedDiffPre(root);
    if (!pre || !pierreDiff) return undefined;

    const split = pre.getAttribute("data-diff-type") === "split";
    const indexes = getPierreLineIndex(pierreDiff)(
      annotation.lineNumber,
      annotation.side as PierreSide,
    );
    if (!indexes) return undefined;

    const lineIndex = split ? indexes[1] : indexes[0];
    const target = renderedLinePair(pre, lineIndex, split, annotation.side as PierreSide);
    if (!target) return undefined;

    const insertedRows: TransientInsertedAnnotationRow[] = [];
    const adjustedColumns = new Map<HTMLElement, number>();
    const targets = transientAnnotationInsertionTargets(pre, target, split);
    const annotationLine = `0,${lineIndex}`;
    const slotName = annotationSlotName(annotation);
    let primaryRow: TransientInsertedAnnotationRow | undefined;
    for (const insertionTarget of targets) {
      const insertedRow = insertAnnotationRowAfter(
        insertionTarget,
        annotationLine,
        insertionTarget.content === target.content ? slotName : undefined,
      );
      if (!insertedRow) continue;
      insertedRows.push(insertedRow);
      if (insertionTarget.content === target.content) {
        primaryRow = insertedRow;
      }
      queueColumnSpanAdjustment(adjustedColumns, insertedRow.content.parentElement, 1);
      queueColumnSpanAdjustment(adjustedColumns, insertedRow.gutter.parentElement, 1);
    }
    applyColumnSpanAdjustments(adjustedColumns);
    primaryRow ??= insertedRows[0];
    if (!primaryRow) return undefined;
    return { ...primaryRow, insertedRows };
  }

  function transientAnnotationInsertionTargets(
    pre: HTMLPreElement,
    target: RenderedLinePair,
    split: boolean,
  ): RenderedLinePair[] {
    if (!split) return [target];
    const targetContentColumn = target.content.parentElement;
    if (!targetContentColumn) return [target];
    const rowIndex = Array.prototype.indexOf.call(targetContentColumn.children, target.content);
    if (rowIndex < 0) return [target];

    const targets: RenderedLinePair[] = [];
    for (const code of renderedCodeColumns(pre)) {
      const [gutter, content] = Array.from(code.children);
      if (!(gutter instanceof HTMLElement) || !(content instanceof HTMLElement)) continue;
      const contentElement = content.children[rowIndex];
      const gutterElement = gutter.children[rowIndex];
      if (!(contentElement instanceof HTMLElement) || !(gutterElement instanceof HTMLElement)) continue;
      targets.push({
        content: contentElement,
        gutter: gutterElement,
        side: renderedPierreCodeSide(code),
      });
    }
    return targets.length > 0 ? targets : [target];
  }

  function insertAnnotationRowAfter(
    target: RenderedLinePair,
    annotationLine: string,
    slotName: string | undefined,
  ): TransientInsertedAnnotationRow | undefined {
    if (!target.content.parentElement || !target.gutter.parentElement) return undefined;
    const gutter = document.createElement("div");
    gutter.setAttribute("data-gutter-buffer", "annotation");
    gutter.setAttribute("data-buffer-size", "1");
    gutter.style.gridRow = "span 1";

    const content = document.createElement("div");
    content.setAttribute("data-line-annotation", annotationLine);
    const annotationContent = document.createElement("div");
    annotationContent.setAttribute("data-annotation-content", "");
    if (slotName) {
      const slot = document.createElement("slot");
      slot.name = slotName;
      annotationContent.appendChild(slot);
    }
    content.appendChild(annotationContent);

    target.gutter.after(gutter);
    target.content.after(content);
    return { content, gutter };
  }

  function queueColumnSpanAdjustment(
    adjustments: Map<HTMLElement, number>,
    column: Element | null,
    delta: number,
  ): void {
    if (!(column instanceof HTMLElement)) return;
    adjustments.set(column, (adjustments.get(column) ?? 0) + delta);
  }

  function applyColumnSpanAdjustments(adjustments: Map<HTMLElement, number>): void {
    for (const [column, delta] of adjustments) {
      const nextSpan = Math.max(1, currentGridRowSpan(column) + delta);
      column.style.setProperty("grid-row", `span ${nextSpan}`);
    }
  }

  function currentGridRowSpan(column: HTMLElement): number {
    const span = /^span\s+(\d+)/.exec(column.style.getPropertyValue("grid-row").trim());
    const rowSpan = span?.[1];
    if (rowSpan) return Number.parseInt(rowSpan, 10);
    return Math.max(1, column.children.length);
  }

  function annotationSlotName(annotation: DiffLineAnnotation<unknown>): string {
    return `annotation-${annotation.side}-${annotation.lineNumber}`;
  }

  function markLineTarget(
    pre: HTMLPreElement,
    indexes: [number, number] | undefined,
    split: boolean,
    side: PierreSide,
    attributes: Record<string, string>,
  ): void {
    if (!indexes) return;
    const lineIndex = split ? indexes[1] : indexes[0];
    if (!Number.isFinite(lineIndex)) return;
    const pair = renderedLinePair(pre, lineIndex, split, side);
    if (!pair) return;
    pair.content.tabIndex = -1;
    pair.gutter.tabIndex = -1;
    pair.content.setAttribute("data-diff-path", renderFile.path);
    pair.gutter.setAttribute("data-diff-path", renderFile.path);
    for (const [name, value] of Object.entries(attributes)) {
      pair.content.setAttribute(name, value);
      pair.gutter.setAttribute(name, value);
    }
  }

  function isHostInScrollViewport(): boolean {
    if (!host) return false;
    const root = host.closest(".diff-area");
    const hostRect = host.getBoundingClientRect();
    const rootRect = root?.getBoundingClientRect() ?? {
      top: 0,
      bottom: window.innerHeight,
    };
    return hostRect.bottom > rootRect.top && hostRect.top < rootRect.bottom;
  }

  function renderedDiffPre(root = host?.shadowRoot): HTMLPreElement | null {
    return root?.querySelector<HTMLPreElement>("pre[data-diff]") ?? null;
  }

  function expandedContextDebugRows(): Array<{
    altLine: string | null;
    line: string | null;
    text: string;
    type: string | null;
  }> {
    const root = host?.shadowRoot;
    if (!root) return [];
    return Array.from(root.querySelectorAll<HTMLElement>("[data-line-type='context-expanded'][data-line]"))
      .slice(0, 12)
      .map((row) => ({
        altLine: row.getAttribute("data-alt-line"),
        line: row.getAttribute("data-line"),
        text: (row.textContent ?? "").trim().slice(0, 120),
        type: row.getAttribute("data-line-type"),
      }));
  }

  function removeStalePlaceholderPres(): void {
    const root = host?.shadowRoot;
    if (!root) return;
    for (const pre of root.querySelectorAll<HTMLPreElement>("pre:not([data-diff])")) {
      if (pre.childElementCount === 0 && !pre.textContent?.trim()) {
        pre.remove();
      }
    }
  }

  function refreshRenderedLineRows(pre: HTMLPreElement, split: boolean): void {
    const next = new Map<number, RenderedLinePair[]>();
    for (const code of renderedCodeColumns(pre)) {
      const side = split ? renderedPierreCodeSide(code) : undefined;
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
        rows.push({ content: contentElement, gutter: gutterElement, side });
        next.set(lineIndex, rows);
      }
    }
    renderedLineRows = next;
  }

  function linePairMatchesSide(
    pair: RenderedLinePair,
    split: boolean,
    side: PierreSide | undefined,
  ): boolean {
    return !split || side == null || pair.side === side;
  }

  function renderedLinePair(
    pre: HTMLPreElement,
    targetIndex: number,
    split: boolean,
    side?: PierreSide,
  ): RenderedLinePair | undefined {
    const cached = renderedLineRows.get(targetIndex)?.find((pair) =>
      linePairMatchesSide(pair, split, side)
    );
    if (cached) return cached;
    for (const code of renderedCodeColumns(pre)) {
      const codeSide = split ? renderedPierreCodeSide(code) : undefined;
      if (split && side != null && codeSide !== side) continue;
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
            return { content: contentElement, gutter: gutterElement, side: codeSide };
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
  aria-busy={!rendered}
>
  <diffs-container
    class="pierre-diff"
    class:pierre-diff--pending={!rendered}
    hidden={emptyTextualDiff}
    bind:this={host}
  ></diffs-container>
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
