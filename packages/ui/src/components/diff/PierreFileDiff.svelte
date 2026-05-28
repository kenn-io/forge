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
  import { onMount } from "svelte";
  import type { DiffFile } from "../../api/types.js";
  import { appThemeType, parsePierreFileDiff } from "./pierre-diff.js";
  import { getPierreDiffWorkerPool } from "./pierre-worker-pool.js";

  interface Props {
    file: DiffFile;
    active?: boolean;
    wordWrap?: boolean;
    loadFileText?: ((side: "old" | "new") => Promise<string>) | undefined;
    lineAnnotations?: DiffLineAnnotation<unknown>[];
    selectedRange?: SelectedLineRange | null;
    enableLineSelection?: boolean;
    onLineSelected?: (selection: SelectedLineRange | null) => void;
    renderAnnotation?: (annotation: DiffLineAnnotation<unknown>) => HTMLElement | undefined;
  }

  const {
    file,
    active = true,
    wordWrap = false,
    loadFileText,
    lineAnnotations = [],
    selectedRange = null,
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
  const inactiveCleanupDelayMs = 10_000;

  const fileKey = $derived(`${file.path}\0${file.old_path}\0${file.patch}`);
  const emptyTextualDiff = $derived(!file.patch.trim() || file.hunks.length === 0);
  const pierreFile = $derived.by<FileDiffMetadata | undefined>(() => {
    return parsePierreFileDiff(file, {
      // Pierre marks patch-only diffs as partial and hides expansion controls.
      // Give it sparse line arrays so the controls render; the first click is
      // intercepted, full contents are fetched, and the same expansion replays.
      enableDemandContextExpansion: Boolean(loadFileText) && hasCollapsedContext(file),
    });
  });

  const pierreOptions = $derived.by<FileDiffOptions<unknown>>(() => ({
    diffStyle: "unified",
    diffIndicators: "bars",
    disableFileHeader: true,
    enableGutterUtility: enableLineSelection,
    enableLineSelection,
    hunkSeparators: "line-info",
    lineDiffType: "word",
    lineHoverHighlight: enableLineSelection ? "both" : "disabled",
    ...(onLineSelected && {
      onGutterUtilityClick: onLineSelected,
      onLineSelected,
    }),
    ...(renderAnnotation && { renderAnnotation }),
    overflow: wordWrap ? "wrap" : "scroll",
    theme: { dark: "pierre-dark", light: "pierre-light" },
    themeType,
    expansionLineCount: 40,
    tokenizeMaxLineLength: 2_000,
    onPostRender: () => {
      applyHunkHeaderLabels();
      rendered = true;
    },
    unsafeCSS: `
      :host {
        display: block;
        font-family: var(--font-mono);
        --diffs-font-family: var(--font-mono);
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
  });

  $effect(() => {
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
      fullContext ? "full" : "patch",
      enableLineSelection,
      annotationKey(lineAnnotations),
    ].join("\0");
    if (renderAttemptKey === nextRenderAttemptKey) {
      pierreDiff.setSelectedLines(selectedRange);
      return;
    }
    renderAttemptKey = nextRenderAttemptKey;
    rendered = false;
    if (fullContext) {
      renderFullContext(fullContext);
    } else {
      const didRender = pierreDiff.render({
        fileContainer: host,
        fileDiff: pierreFile,
        forceRender: true,
        lineAnnotations,
      });
      if (didRender) {
        applyHunkHeaderLabels();
        rendered = true;
        placeholderHeight = 0;
        installDemandContextHandler();
      }
    }
    pierreDiff.setSelectedLines(selectedRange);
  });

  $effect(() => {
    if (active && pierreDiff && pierreFile) {
      pierreDiff.setThemeType(themeType);
    }
  });

  $effect(() => {
    pierreDiff?.setSelectedLines(selectedRange);
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
    pierreDiff?.cleanUp();
    pierreDiff = undefined;
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
    const context = await loadFullContext();
    renderFullContext(context);
    pierreDiff?.expandHunk(hunkIndex, direction, expansionLineCount);
    applyHunkHeaderLabels();
  }

  function renderFullContext(context: { oldFile: FileContents; newFile: FileContents }): void {
    if (!pierreDiff || !host) return;
    rendered = false;
    const didRender = pierreDiff.render({
      fileContainer: host,
      oldFile: context.oldFile,
      newFile: context.newFile,
      forceRender: true,
      lineAnnotations,
    });
    pierreDiff.setSelectedLines(selectedRange);
    if (didRender) {
      applyHunkHeaderLabels();
      rendered = true;
      placeholderHeight = 0;
    }
  }

  async function loadFullContext(): Promise<{ oldFile: FileContents; newFile: FileContents }> {
    if (fullContext) return fullContext;
    contextLoadPromise ??= fetchFullContext();
    fullContext = await contextLoadPromise;
    return fullContext;
  }

  async function fetchFullContext(): Promise<{ oldFile: FileContents; newFile: FileContents }> {
    if (!loadFileText) {
      throw new Error("Context loading is unavailable");
    }
    contextError = null;
    const [oldContents, newContents] = await Promise.all([
      file.status === "added" ? Promise.resolve("") : loadFileText("old"),
      file.status === "deleted" ? Promise.resolve("") : loadFileText("new"),
    ]);
    return {
      oldFile: {
        name: file.old_path || file.path,
        contents: oldContents,
      },
      newFile: {
        name: file.path,
        contents: newContents,
      },
    };
  }

  function annotationKey(annotations: DiffLineAnnotation<unknown>[]): string {
    return annotations.map((annotation) => {
      const metadata = annotation.metadata as { id?: unknown } | undefined;
      return `${annotation.side}:${annotation.lineNumber}:${String(metadata?.id ?? "")}`;
    }).join("|");
  }

  function hasCollapsedContext(f: DiffFile): boolean {
    let previousOldEnd = 1;
    for (const hunk of f.hunks) {
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
      nextSeparatorHunkIndex = Math.max(nextSeparatorHunkIndex, hunkIndex + 1);
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
