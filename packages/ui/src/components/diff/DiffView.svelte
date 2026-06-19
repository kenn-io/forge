<script lang="ts">
  import { Virtualizer } from "@pierre/diffs";
  import { onMount, tick, untrack } from "svelte";
  import { getStores } from "../../context.js";
  import type { DiffScrollTarget } from "../../stores/diff.svelte.js";
  import type { DiffReviewDraftComment } from "../../stores/diff-review-draft.svelte.js";
  import type { ReviewThread } from "./review-thread-context.js";

  const stores = getStores();
  const diffStore = stores.diff;
  const diffReviewDraft = stores.diffReviewDraft;
  import DiffFileComponent from "./DiffFile.svelte";
  import DiffReviewDraftTray from "./DiffReviewDraftTray.svelte";

  interface Props {
    owner: string;
    name: string;
    number: number;
    loadOnMount?: boolean;
    keyboardActive?: boolean;
    pageKeyboardActive?: boolean;
    richPreviewEnabled?: boolean;
    contextExpansionEnabled?: boolean;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    reviewMode?: "enabled" | "disabled";
    diffHeadSHA?: string | undefined;
    reviewDraftMutation?: boolean;
    canReplyToThreads?: boolean;
    /** Why review authoring is unavailable despite the provider
     * supporting it (missing write credential, rate limit). Rendered
     * where the draft tray would otherwise appear. */
    reviewUnavailableReason?: string | undefined;
    supportedReviewActions?: string[];
    nativeMultilineRanges?: boolean;
    reviewThreads?: ReviewThread[];
    initialScrollTop?: number;
    onScrollTopChange?: ((scrollTop: number) => void) | undefined;
  }

  const {
    owner,
    name,
    number,
    loadOnMount = true,
    keyboardActive = true,
    pageKeyboardActive = keyboardActive,
    richPreviewEnabled = true,
    contextExpansionEnabled = true,
    provider,
    platformHost,
    repoPath,
    reviewMode = "enabled",
    diffHeadSHA = undefined,
    reviewDraftMutation = false,
    canReplyToThreads = false,
    reviewUnavailableReason = undefined,
    supportedReviewActions = [],
    nativeMultilineRanges = false,
    reviewThreads = [],
    initialScrollTop = 0,
    onScrollTopChange,
  }: Props = $props();

  let diffArea: HTMLDivElement | undefined = $state();
  let diffContent: HTMLDivElement | undefined = $state();
  let diffVirtualizer: Virtualizer | undefined = $state();
  let scrollClearRaf = 0;
  let scrollRestoreRaf = 0;
  let scrollTargetRaf = 0;
  let virtualizerWakeRaf = 0;
  let scrollTargetRun = 0;
  let scrollingToTarget: DiffScrollTarget | null = null;
  let restoredScrollScope = "";
  const maxScrollTargetAttempts = 240;
  const requiredFileTargetVisibleFrames = 20;
  const requiredLineTargetVisibleFrames = 2;

  onMount(() => {
    if (loadOnMount) {
      void diffStore.loadDiff(owner, name, number, {
        provider,
        platformHost,
        owner,
        name,
        repoPath,
      });
    }

    return () => {
      scrollTargetRun += 1;
      cancelAnimationFrame(scrollClearRaf);
      cancelAnimationFrame(scrollRestoreRaf);
      cancelAnimationFrame(scrollTargetRaf);
      cancelAnimationFrame(virtualizerWakeRaf);
      diffStore.clearDiff();
      diffReviewDraft?.clear();
    };
  });

  const diff = $derived(diffStore.getDiff());
  const visibleFiles = $derived(diffStore.getVisibleDiffFiles());
  const navigationFiles = $derived(
    diffStore.getVisibleFileList()?.files ?? visibleFiles,
  );
  const loading = $derived(diffStore.isDiffLoading());
  const error = $derived(diffStore.getDiffError());
  const reviewWarning = $derived(diffReviewDraft?.getWarning() ?? null);
  const tabWidth = $derived(diffStore.getTabWidth());
  const wordWrap = $derived(diffStore.getWordWrap());
  const scopeKind = $derived(
    "getScope" in diffStore ? diffStore.getScope().kind : "head",
  );
  const reviewEnabled = $derived(
    reviewMode === "enabled" &&
      reviewDraftMutation &&
      supportedReviewActions.length > 0 &&
      scopeKind === "head" &&
      !!diffHeadSHA &&
      !diff?.stale,
  );
  const diffScrollScopeKey = $derived(
    `${provider}\0${platformHost ?? ""}\0${repoPath}\0${number}\0${diffHeadSHA ?? ""}`,
  );

  $effect(() => {
    const nextRef = { provider, platformHost, owner, name, repoPath };
    const nextNumber = number;
    const nextReviewEnabled = reviewEnabled;
    const nextDiffHeadSHA = diffHeadSHA;
    untrack(() => {
      diffReviewDraft?.setContext(
        nextRef,
        nextNumber,
        nextReviewEnabled,
        nextDiffHeadSHA,
      );
    });
  });

  $effect(() => {
    const area = diffArea;
    const restoreKey = diffScrollScopeKey;
    const restoreTop = initialScrollTop;
    if (!area || !diff || loading || restoredScrollScope === restoreKey) return;
    restoredScrollScope = restoreKey;
    cancelAnimationFrame(scrollRestoreRaf);
    scrollRestoreRaf = requestAnimationFrame(() => {
      scrollRestoreRaf = 0;
      if (diffArea !== area) return;
      area.scrollTop = Math.max(0, restoreTop);
      wakeDiffVirtualizer();
    });
  });

  $effect(() => {
    const area = diffArea;
    const content = diffContent;
    if (
      !area ||
      !content ||
      typeof ResizeObserver === "undefined" ||
      typeof IntersectionObserver === "undefined"
    ) {
      diffVirtualizer = undefined;
      return;
    }

    const virtualizer = new Virtualizer();
    virtualizer.setup(area, content);
    diffVirtualizer = virtualizer;

    return () => {
      virtualizer.cleanUp();
      if (diffVirtualizer === virtualizer) {
        diffVirtualizer = undefined;
      }
    };
  });

  function scrollWithinDiffArea(el: Element, offset = 0): void {
    if (!diffArea) return;
    const areaRect = diffArea.getBoundingClientRect();
    const elRect = el.getBoundingClientRect();
    diffArea.scrollTop += elRect.top - areaRect.top - offset;
    wakeDiffVirtualizer();
  }

  function diffFileElement(path: string): HTMLElement | null {
    if (!diffArea) return null;
    for (const el of diffArea.querySelectorAll<HTMLElement>("[data-file-path]")) {
      if (el.dataset.filePath === path) return el;
    }
    return null;
  }

  function isFileVisible(path: string): boolean {
    if (!diffArea) return false;
    const el = diffFileElement(path);
    if (!el) return false;
    const areaRect = diffArea.getBoundingClientRect();
    const elRect = el.getBoundingClientRect();
    return elRect.bottom > areaRect.top && elRect.top < areaRect.bottom;
  }

  function scrollToFile(path: string): boolean {
    if (!diffArea) return false;
    const el = diffFileElement(path);
    if (el) {
      const areaRect = diffArea.getBoundingClientRect();
      const elRect = el.getBoundingClientRect();
      if (areaRect.height <= 0 || elRect.height <= 0) {
        return false;
      }
      diffArea.scrollTop += elRect.top - areaRect.top;
      wakeDiffVirtualizer();
    } else {
      return false;
    }
    return isFileVisible(path);
  }

  function finishProgrammaticScroll(): void {
    // Clear the scrolling flag after the instant scroll so the next user-initiated
    // scroll event resumes active file tracking.
    scrollClearRaf = requestAnimationFrame(() => diffStore.clearScrolling());
  }

  function cancelPendingProgrammaticScroll(): void {
    scrollTargetRun += 1;
    scrollingToTarget = null;
    diffStore.consumeScrollTarget();
    diffStore.clearScrolling();
    cancelAnimationFrame(scrollClearRaf);
    scrollClearRaf = 0;
  }

  function cancelProgrammaticScrollIfUserOverrides(): void {
    if (
      scrollingToTarget ||
      normalizeScrollTarget(diffStore.getScrollTarget()) ||
      diffStore.isScrolling()
    ) {
      cancelPendingProgrammaticScroll();
    }
  }

  function queryDiffElement(selector: string): HTMLElement | null {
    if (!diffArea) return null;
    const lightMatch = diffArea.querySelector<HTMLElement>(selector);
    if (lightMatch) return lightMatch;
    for (const host of diffArea.querySelectorAll<HTMLElement>("*")) {
      const match = host.shadowRoot?.querySelector<HTMLElement>(selector);
      if (match) return match;
    }
    return null;
  }

  function scrollToTarget(target: DiffScrollTarget): boolean {
    if (!diffArea) return false;
    if (target.line == null) return scrollToFile(target.path);

    const attr = target.side === "left" ? "data-diff-old-line" : "data-diff-new-line";
    const lineEl = queryDiffElement(
      `[data-diff-path="${CSS.escape(target.path)}"][${attr}="${CSS.escape(String(target.line))}"]`,
    );
    if (!lineEl) {
      void scrollToFile(target.path);
      return false;
    }

    scrollWithinDiffArea(lineEl, 72);
    lineEl.focus({ preventScroll: true });
    return true;
  }

  function isScrollTargetVisible(target: DiffScrollTarget): boolean {
    if (!diffArea) return false;
    if (target.line == null) return isFileVisible(target.path);

    const attr = target.side === "left" ? "data-diff-old-line" : "data-diff-new-line";
    const lineEl = queryDiffElement(
      `[data-diff-path="${CSS.escape(target.path)}"][${attr}="${CSS.escape(String(target.line))}"]`,
    );
    if (!lineEl) return false;

    const areaRect = diffArea.getBoundingClientRect();
    const elRect = lineEl.getBoundingClientRect();
    return areaRect.height > 0 &&
      elRect.height > 0 &&
      elRect.bottom > areaRect.top &&
      elRect.top < areaRect.bottom;
  }

  function isScrollTargetReady(target: DiffScrollTarget): boolean {
    if (target.line != null) return true;
    const el = diffFileElement(target.path);
    if (!el) return false;
    if (el.querySelector(".binary-notice, .empty-textual-diff")) return true;
    const shell = el.querySelector(".pierre-diff-shell");
    if (shell?.getAttribute("aria-busy") === "true") return false;
    const host = el.querySelector<HTMLElement>(".pierre-diff");
    return !host || host.shadowRoot?.querySelector("pre[data-diff]") != null;
  }

  function jumpToDraftComment(comment: DiffReviewDraftComment): void {
    if (!diffArea) return;
    const el = queryDiffElement(
      `[data-draft-comment-id="${CSS.escape(comment.id)}"]`,
    );
    if (!el) {
      void scrollToFile(comment.path);
      return;
    }
    const areaRect = diffArea.getBoundingClientRect();
    const elRect = el.getBoundingClientRect();
    diffArea.scrollTop += elRect.top - areaRect.top - 72;
    wakeDiffVirtualizer();
    el.focus({ preventScroll: true });
    finishProgrammaticScroll();
  }

  function wakeDiffVirtualizer(): void {
    const area = diffArea;
    if (!area) return;
    area.dispatchEvent(new Event("scroll"));
    cancelAnimationFrame(virtualizerWakeRaf);
    virtualizerWakeRaf = requestAnimationFrame(() => {
      virtualizerWakeRaf = 0;
      if (diffArea === area) {
        area.dispatchEvent(new Event("scroll"));
      }
    });
  }

  // Watch for scroll requests from the sidebar file list (via the store).
  // Only consume the target once diffArea is mounted and diff data is available,
  // so the request is not lost if the user clicks a file before diff renders.
  $effect(() => {
    const target = normalizeScrollTarget(diffStore.getScrollTarget());
    if (!target) {
      scrollingToTarget = null;
      return;
    }
    if (target && diffArea && diff) {
      if (scrollingToTarget && sameScrollTarget(scrollingToTarget, target)) return;
      scrollingToTarget = target;
      const run = scrollTargetRun + 1;
      scrollTargetRun = run;
      void scrollToTargetAfterDom(target, run);
    }
  });

  async function scrollToTargetAfterDom(
    target: DiffScrollTarget,
    run: number,
  ): Promise<void> {
    await tick();
    if (
      scrollTargetRun !== run ||
      !sameScrollTarget(normalizeScrollTarget(diffStore.getScrollTarget()), target)
    ) return;

    const requiredVisibleFrames = target.line == null
      ? requiredFileTargetVisibleFrames
      : requiredLineTargetVisibleFrames;
    let visibleFrames = 0;
    let targetReached = false;
    for (let attempt = 0; attempt < maxScrollTargetAttempts; attempt += 1) {
      await nextAnimationFrame();
      if (
        scrollTargetRun !== run ||
        !sameScrollTarget(normalizeScrollTarget(diffStore.getScrollTarget()), target)
      ) return;
      if (!targetReached) {
        targetReached = scrollToTarget(target);
      }
      if (!targetReached) {
        visibleFrames = 0;
        continue;
      }
      if (isScrollTargetVisible(target) && isScrollTargetReady(target)) {
        if (target.line == null) {
          targetReached = scrollToTarget(target);
          if (!targetReached) {
            visibleFrames = 0;
            continue;
          }
        }
        visibleFrames += 1;
      } else {
        targetReached = false;
        visibleFrames = 0;
      }
      if (visibleFrames >= requiredVisibleFrames) {
        scrollToTarget(target);
        diffStore.consumeScrollTarget();
        scrollingToTarget = null;
        finishProgrammaticScroll();
        return;
      }
    }
    if (
      scrollTargetRun === run &&
      sameScrollTarget(normalizeScrollTarget(diffStore.getScrollTarget()), target)
    ) {
      diffStore.consumeScrollTarget();
      scrollingToTarget = null;
      finishProgrammaticScroll();
    }
  }

  function nextAnimationFrame(): Promise<void> {
    return new Promise((resolve) => {
      scrollTargetRaf = requestAnimationFrame(() => {
        scrollTargetRaf = 0;
        resolve();
      });
    });
  }

  function sameScrollTarget(
    left: DiffScrollTarget | null,
    right: DiffScrollTarget,
  ): boolean {
    return !!left &&
      left.path === right.path &&
      left.line === right.line &&
      left.side === right.side;
  }

  function normalizeScrollTarget(
    target: DiffScrollTarget | string | null,
  ): DiffScrollTarget | null {
    if (typeof target === "string") return { path: target };
    return target;
  }

  // Scroll-based active file tracking.
  // Skipped for one frame after programmatic scroll to avoid re-setting activeFile.
  function onDiffScroll(): void {
    if (diffArea) {
      onScrollTopChange?.(diffArea.scrollTop);
    }
    if (!diffArea || !diff) return;
    if (diffStore.isScrolling()) return;
    const rect = diffArea.getBoundingClientRect();
    const threshold = rect.top + 60;

    let current: string | null = null;
    for (const file of visibleFiles) {
      const el = diffFileElement(file.path);
      if (!el) continue;
      const elRect = el.getBoundingClientRect();
      if (elRect.top <= threshold) {
        current = file.path;
      }
    }
    if (current !== null) {
      diffStore.setActiveFile(current);
    }
  }

  function isTextEntryTarget(target: EventTarget | null): boolean {
    if (!(target instanceof HTMLElement)) return false;
    const tag = target.tagName;
    return tag === "INPUT" ||
      tag === "TEXTAREA" ||
      tag === "SELECT" ||
      target.isContentEditable;
  }

  function pageDiffArea(direction: 1 | -1): void {
    const area = diffArea;
    if (!area) return;
    cancelPendingProgrammaticScroll();
    const maxTop = Math.max(0, area.scrollHeight - area.clientHeight);
    const pageSize = Math.max(1, area.clientHeight - 64);
    area.scrollTop = Math.min(maxTop, Math.max(0, area.scrollTop + pageSize * direction));
    area.focus({ preventScroll: true });
    wakeDiffVirtualizer();
    onScrollTopChange?.(area.scrollTop);
  }

  function shouldContainWheelAtDiffBoundary(
    event: WheelEvent,
    area: HTMLElement,
  ): boolean {
    if (event.deltaY === 0) return false;

    const maxScrollTop = Math.max(0, area.scrollHeight - area.clientHeight);
    if (maxScrollTop === 0) return true;
    if (event.deltaY < 0) return area.scrollTop <= 0;
    return area.scrollTop >= maxScrollTop - 1;
  }

  function onDiffUserScrollIntent(event: Event): void {
    cancelProgrammaticScrollIfUserOverrides();
    if (!(event instanceof WheelEvent)) return;

    const area = event.currentTarget instanceof HTMLElement
      ? event.currentTarget
      : diffArea;
    if (!area || !shouldContainWheelAtDiffBoundary(event, area)) return;
    event.preventDefault();
  }

  function handlePageKeydown(e: KeyboardEvent): boolean {
    if (isTextEntryTarget(e.target)) return false;

    if (e.key === "PageDown" || e.key === "PageUp") {
      if (e.metaKey || e.ctrlKey || e.altKey || !diff) return false;
      e.preventDefault();
      pageDiffArea(e.key === "PageDown" ? 1 : -1);
      return true;
    }
    return false;
  }

  // j/k keyboard navigation between files; PageUp/PageDown page the diff pane.
  function handleKeydown(e: KeyboardEvent): void {
    if (handlePageKeydown(e)) return;
    if (isTextEntryTarget(e.target)) return;

    if (e.key === "j" || e.key === "k") {
      if (!diff || navigationFiles.length === 0) return;
      e.preventDefault();
      const paths = navigationFiles.map((f) => f.path);
      const currentIdx = diffStore.getActiveFile() ? paths.indexOf(diffStore.getActiveFile()!) : -1;
      let nextIdx: number;
      if (e.key === "j") {
        nextIdx = currentIdx < paths.length - 1 ? currentIdx + 1 : currentIdx;
      } else {
        nextIdx = currentIdx > 0 ? currentIdx - 1 : 0;
      }
      const nextPath = paths[nextIdx] ?? null;
      if (nextPath) diffStore.requestScrollToFile(nextPath);
    }

    if (e.key === "[" || e.key === "]") {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      e.preventDefault();
      if (e.key === "[") {
        diffStore.stepPrev();
      } else {
        diffStore.stepNext();
      }
    }
  }

  function handleDiffAreaKeydown(e: KeyboardEvent): void {
    if (!pageKeyboardActive || keyboardActive) return;
    handlePageKeydown(e);
  }

  $effect(() => {
    if (!keyboardActive) return;
    window.addEventListener("keydown", handleKeydown);
    return () => window.removeEventListener("keydown", handleKeydown);
  });

  $effect(() => {
    const area = diffArea;
    if (!area) return;

    area.tabIndex = -1;
    area.addEventListener("wheel", onDiffUserScrollIntent, { passive: false });
    area.addEventListener("touchstart", onDiffUserScrollIntent);
    area.addEventListener("pointerdown", onDiffUserScrollIntent);
    area.addEventListener("keydown", handleDiffAreaKeydown);

    return () => {
      area.removeEventListener("wheel", onDiffUserScrollIntent);
      area.removeEventListener("touchstart", onDiffUserScrollIntent);
      area.removeEventListener("pointerdown", onDiffUserScrollIntent);
      area.removeEventListener("keydown", handleDiffAreaKeydown);
    };
  });
</script>

<div class="diff-view">
  {#if diff?.stale}
    <div class="stale-banner">
      Diff may be outdated -- showing changes as of an earlier version of this PR.
    </div>
  {/if}

  <div class="diff-body">
    {#if loading && !diff}
      <div class="diff-state">
        <svg class="diff-spinner" width="20" height="20" viewBox="0 0 20 20" fill="none">
          <circle cx="10" cy="10" r="8" stroke="currentColor" stroke-opacity="0.2" stroke-width="2" />
          <path d="M18 10a8 8 0 0 0-8-8" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
        </svg>
        <p class="diff-state-msg">Loading diff</p>
      </div>
    {:else if error}
      <div class="diff-state">
        <p class="diff-state-msg diff-state-msg--error">{error}</p>
      </div>
    {:else if diff}
      <div class="diff-main">
        <div
          class="diff-area"
          class:diff-area--word-wrap={wordWrap}
          bind:this={diffArea}
          onscroll={onDiffScroll}
          role="region"
          aria-label="Changed file diffs"
          style:tab-size={tabWidth}
          style:overscroll-behavior="contain"
        >
          <div class="diff-content" bind:this={diffContent}>
            {#if visibleFiles.length === 0}
              <div class="diff-state diff-state--empty">
                <p class="diff-state-msg">No changed files match this category.</p>
              </div>
            {/if}
            {#each visibleFiles as file (file.path)}
              <DiffFileComponent
                {file}
                {provider}
                {platformHost}
                {owner}
                {name}
                {repoPath}
                {number}
                {richPreviewEnabled}
                {contextExpansionEnabled}
                {reviewEnabled}
                canReplyToThreads={canReplyToThreads && !diff?.stale}
                {diffHeadSHA}
                {nativeMultilineRanges}
                {reviewThreads}
                virtualizer={diffVirtualizer}
              />
            {/each}
            {#if reviewEnabled && diffReviewDraft}
              {#if reviewWarning}
                <div class="review-warning">{reviewWarning}</div>
              {/if}
              <DiffReviewDraftTray onjump={jumpToDraftComment} />
            {:else if reviewUnavailableReason}
              <div class="review-warning">{reviewUnavailableReason}</div>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

<style>
  .diff-view {
    display: flex;
    flex-direction: column;
    flex: 1;
    overflow: hidden;
    background: var(--diff-bg);
  }

  .stale-banner {
    padding: 6px 16px;
    background: var(--diff-stale-bg);
    color: var(--diff-stale-text);
    border-bottom: 1px solid var(--diff-stale-border);
    font-size: var(--font-size-sm);
    flex-shrink: 0;
  }

  .review-warning {
    flex-shrink: 0;
    padding: 8px 12px;
    border-top: 1px solid var(--diff-stale-border);
    background: var(--diff-stale-bg);
    color: var(--diff-stale-text);
    font-size: var(--font-size-sm);
  }

  .diff-body {
    display: flex;
    flex: 1;
    overflow: hidden;
  }

  .diff-main {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
    overflow: hidden;
  }

  .diff-area {
    flex: 1;
    overflow: auto;
  }

  .diff-content {
    min-width: 0;
  }

  .diff-state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    flex: 1;
  }

  .diff-state--empty {
    min-height: 180px;
  }

  .diff-spinner {
    animation: spin 0.8s linear infinite;
    color: var(--text-muted);
  }

  .diff-state-msg {
    font-size: var(--font-size-md);
    color: var(--text-muted);
  }

  .diff-state-msg--error {
    color: var(--accent-red);
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
