<script lang="ts">
  import { onMount, untrack } from "svelte";
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
    richPreviewEnabled?: boolean;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    reviewMode?: "enabled" | "disabled";
    diffHeadSHA?: string | undefined;
    reviewDraftMutation?: boolean;
    supportedReviewActions?: string[];
    nativeMultilineRanges?: boolean;
    reviewThreads?: ReviewThread[];
  }

  const {
    owner,
    name,
    number,
    loadOnMount = true,
    keyboardActive = true,
    richPreviewEnabled = true,
    provider,
    platformHost,
    repoPath,
    reviewMode = "enabled",
    diffHeadSHA = undefined,
    reviewDraftMutation = false,
    supportedReviewActions = [],
    nativeMultilineRanges = false,
    reviewThreads = [],
  }: Props = $props();

  let diffArea: HTMLDivElement | undefined = $state();
  let scrollRaf = 0;

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
      cancelAnimationFrame(scrollRaf);
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

  function scrollWithinDiffArea(el: Element, offset = 0): void {
    if (!diffArea) return;
    const areaRect = diffArea.getBoundingClientRect();
    const elRect = el.getBoundingClientRect();
    diffArea.scrollTop += elRect.top - areaRect.top - offset;
  }

  function scrollToFile(path: string): boolean {
    if (!diffArea) return false;
    const el = diffArea.querySelector(`[data-file-path="${CSS.escape(path)}"]`);
    if (el) {
      scrollWithinDiffArea(el);
    } else {
      return false;
    }
    // Clear the scrolling flag after the instant scroll so the next user-initiated
    // scroll event resumes active file tracking.
    scrollRaf = requestAnimationFrame(() => diffStore.clearScrolling());
    return true;
  }

  function scrollToTarget(target: DiffScrollTarget): boolean {
    if (!diffArea) return false;
    if (target.line == null) return scrollToFile(target.path);

    const attr = target.side === "left" ? "data-diff-old-line" : "data-diff-new-line";
    const lineEl = diffArea.querySelector(
      `[data-diff-path="${CSS.escape(target.path)}"][${attr}="${CSS.escape(String(target.line))}"]`,
    ) as HTMLElement | null;
    if (!lineEl) return scrollToFile(target.path);

    scrollWithinDiffArea(lineEl, 72);
    lineEl.focus({ preventScroll: true });
    scrollRaf = requestAnimationFrame(() => diffStore.clearScrolling());
    return true;
  }

  function jumpToDraftComment(comment: DiffReviewDraftComment): void {
    if (!diffArea) return;
    const el = diffArea.querySelector(
      `[data-draft-comment-id="${CSS.escape(comment.id)}"]`,
    ) as HTMLElement | null;
    if (!el) {
      void scrollToFile(comment.path);
      return;
    }
    const areaRect = diffArea.getBoundingClientRect();
    const elRect = el.getBoundingClientRect();
    diffArea.scrollTop += elRect.top - areaRect.top - 72;
    el.focus({ preventScroll: true });
    scrollRaf = requestAnimationFrame(() => diffStore.clearScrolling());
  }

  // Watch for scroll requests from the sidebar file list (via the store).
  // Only consume the target once diffArea is mounted and diff data is available,
  // so the request is not lost if the user clicks a file before diff renders.
  $effect(() => {
    const target = diffStore.getScrollTarget();
    if (target && diffArea && diff) {
      if (scrollToTarget(target)) {
        diffStore.consumeScrollTarget();
      }
    }
  });

  // Scroll-based active file tracking.
  // Skipped for one frame after programmatic scroll to avoid re-setting activeFile.
  function onDiffScroll(): void {
    if (!diffArea || !diff) return;
    if (diffStore.isScrolling()) return;
    const rect = diffArea.getBoundingClientRect();
    const threshold = rect.top + 60;

    let current: string | null = null;
    for (const file of visibleFiles) {
      const el = diffArea.querySelector(`[data-file-path="${CSS.escape(file.path)}"]`);
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

  // j/k keyboard navigation between files.
  function handleKeydown(e: KeyboardEvent): void {
    const tag = (e.target as HTMLElement).tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
    if ((e.target as HTMLElement).isContentEditable) return;

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

  $effect(() => {
    if (!keyboardActive) return;
    window.addEventListener("keydown", handleKeydown);
    return () => window.removeEventListener("keydown", handleKeydown);
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
          style:tab-size={tabWidth}
        >
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
              {reviewEnabled}
              {diffHeadSHA}
              {nativeMultilineRanges}
              {reviewThreads}
            />
          {/each}
          {#if reviewEnabled && diffReviewDraft}
            {#if reviewWarning}
              <div class="review-warning">{reviewWarning}</div>
            {/if}
            <DiffReviewDraftTray onjump={jumpToDraftComment} />
          {/if}
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
