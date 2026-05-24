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

  interface Props {
    file: DiffFile;
    active?: boolean;
    wordWrap?: boolean;
    loadFileText?: (side: "old" | "new") => Promise<string>;
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
  let renderedFileKey = "";

  const fileKey = $derived(`${file.path}\0${file.old_path}\0${file.patch}`);
  const pierreFile = $derived.by<FileDiffMetadata | undefined>(() => {
    return parsePierreFileDiff(file, {
      // Pierre marks patch-only diffs as partial and hides expansion controls.
      // Give it sparse line arrays so the controls render; the first click is
      // intercepted, full contents are fetched, and the same expansion replays.
      enableDemandContextExpansion: hasCollapsedContext(file),
    });
  });

  const pierreOptions = $derived<FileDiffOptions<unknown>>({
    diffStyle: "unified",
    diffIndicators: "bars",
    disableFileHeader: true,
    enableGutterUtility: enableLineSelection,
    enableLineSelection,
    hunkSeparators: "line-info",
    lineDiffType: "word",
    lineHoverHighlight: enableLineSelection ? "both" : "disabled",
    onGutterUtilityClick: onLineSelected,
    onLineSelected,
    renderAnnotation,
    overflow: wordWrap ? "wrap" : "scroll",
    theme: { dark: "pierre-dark", light: "pierre-light" },
    themeType,
    expansionLineCount: 40,
    tokenizeMaxLineLength: 2_000,
    onPostRender: () => {
      applyHunkHeaderLabels();
    },
    unsafeCSS: `
      :host {
        display: block;
        font-family: var(--font-mono);
        --diffs-font-family: var(--font-mono);
        --diffs-light-bg: var(--bg-surface, #fff);
        --diffs-dark-bg: var(--bg-surface, #16161e);
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
      removeDemandContextHandler();
      contextLoadPromise = undefined;
      pierreDiff?.cleanUp();
      pierreDiff = undefined;
    };
  });

  $effect(() => {
    if (renderedFileKey === fileKey) return;
    renderedFileKey = fileKey;
    removeDemandContextHandler();
    contextLoadPromise = undefined;
    contextError = null;
    fullContext = undefined;
  });

  $effect(() => {
    if (!host || !active || !pierreFile) return;
    pierreDiff ??= new FileDiff<unknown>(pierreOptions);
    pierreDiff.setOptions(pierreOptions);
    if (fullContext) {
      renderFullContext(fullContext);
    } else {
      pierreDiff.render({
        fileContainer: host,
        fileDiff: pierreFile,
        forceRender: true,
        lineAnnotations,
      });
      applyHunkHeaderLabels();
      installDemandContextHandler();
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
    pierreDiff.render({
      fileContainer: host,
      oldFile: context.oldFile,
      newFile: context.newFile,
      forceRender: true,
      lineAnnotations,
    });
    pierreDiff.setSelectedLines(selectedRange);
    applyHunkHeaderLabels();
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
        cacheKey: `middleman:${file.path}:old`,
      },
      newFile: {
        name: file.path,
        contents: newContents,
        cacheKey: `middleman:${file.path}:new`,
      },
    };
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

<diffs-container class="pierre-diff" bind:this={host}></diffs-container>
{#if contextError}
  <div class="context-error">Could not load more context: {contextError}</div>
{/if}

<style>
  .pierre-diff {
    min-width: 100%;
    width: 100%;
  }

  .pierre-diff:empty {
    min-height: 48px;
  }

  .context-error {
    padding: 6px 12px;
    color: var(--danger);
    border-top: 1px solid var(--diff-border);
    font-size: var(--font-size-xs);
  }
</style>
