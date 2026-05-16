<script lang="ts">
  import SplitResizeHandle from "../shared/SplitResizeHandle.svelte";
  import type { ProviderCapabilities } from "../../api/types.js";
  import type { SplitResizeEvent } from "../shared/split-resize.js";
  import DiffSidebar from "./DiffSidebar.svelte";
  import DiffToolbar from "./DiffToolbar.svelte";
  import DiffView from "./DiffView.svelte";

  interface Props {
    provider: string;
    platformHost?: string | undefined;
    owner: string;
    name: string;
    repoPath: string;
    number: number;
    diffHeadSHA?: string | undefined;
    capabilities?: ProviderCapabilities | undefined;
  }

  const {
    provider,
    platformHost,
    owner,
    name,
    repoPath,
    number,
    diffHeadSHA = undefined,
    capabilities = undefined,
  }: Props = $props();

  const storageKey = "diff-file-tree-width";
  const defaultFileTreeWidth = 280;
  const minFileTreeWidth = 200;
  const maxFileTreeWidth = 520;
  const minDiffPaneWidth = 320;
  const resizeHandleWidth = 4;
  let fileTreeResizeStartWidth = 0;

  function safeGetItem(key: string): string | null {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  }

  function safeSetItem(key: string, value: string): void {
    try {
      localStorage.setItem(key, value);
    } catch {
      /* ignore */
    }
  }

  function layoutMaxFileTreeWidth(): number {
    if (filesLayoutWidth <= 0) {
      return maxFileTreeWidth;
    }
    return Math.max(
      0,
      filesLayoutWidth - minDiffPaneWidth - resizeHandleWidth,
    );
  }

  function minAllowedFileTreeWidth(): number {
    return Math.min(minFileTreeWidth, layoutMaxFileTreeWidth());
  }

  function maxAllowedFileTreeWidth(): number {
    return Math.min(maxFileTreeWidth, layoutMaxFileTreeWidth());
  }

  function clampFileTreeWidth(width: number): number {
    return Math.max(
      minAllowedFileTreeWidth(),
      Math.min(maxAllowedFileTreeWidth(), Math.round(width)),
    );
  }

  function loadFileTreeWidth(): number {
    const raw = Number.parseInt(safeGetItem(storageKey) ?? "", 10);
    if (!Number.isFinite(raw)) return defaultFileTreeWidth;
    return clampFileTreeWidth(raw);
  }

  let filesLayout: HTMLDivElement | undefined = $state();
  let filesLayoutWidth = $state(0);
  let fileTreeWidth = $state(loadFileTreeWidth());

  function saveFileTreeWidth(width: number): void {
    safeSetItem(storageKey, String(width));
  }

  function updateFilesLayoutWidth(width: number): void {
    if (!Number.isFinite(width) || width <= 0) return;
    filesLayoutWidth = Math.round(width);
    fileTreeWidth = clampFileTreeWidth(fileTreeWidth);
  }

  function handleFileTreeResizeStart(): void {
    fileTreeResizeStartWidth = fileTreeWidth;
  }

  function widthFromResize(event: SplitResizeEvent): number {
    return clampFileTreeWidth(
      fileTreeResizeStartWidth + event.deltaX,
    );
  }

  function handleFileTreeResize(event: SplitResizeEvent): void {
    fileTreeWidth = widthFromResize(event);
  }

  function handleFileTreeResizeEnd(event: SplitResizeEvent): void {
    const finalWidth = widthFromResize(event);
    fileTreeWidth = finalWidth;
    saveFileTreeWidth(finalWidth);
  }

  $effect(() => {
    const layout = filesLayout;
    if (!layout) return;

    updateFilesLayoutWidth(layout.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;

    const observer = new ResizeObserver((entries) => {
      updateFilesLayoutWidth(
        entries[0]?.contentRect.width ?? layout.getBoundingClientRect().width,
      );
    });
    observer.observe(layout);

    return () => {
      observer.disconnect();
    };
  });

</script>

<div class="files-view">
  <DiffToolbar />
  <div class="files-layout" bind:this={filesLayout}>
    <aside
      class="files-sidebar"
      aria-label="Changed files"
      style:--diff-file-tree-width={`${fileTreeWidth}px`}
    >
      <DiffSidebar showCommits={false} />
    </aside>
    <SplitResizeHandle
      class="files-resize-handle"
      ariaLabel="Resize file tree"
      onResizeStart={handleFileTreeResizeStart}
      onResize={handleFileTreeResize}
      onResizeEnd={handleFileTreeResizeEnd}
    />
    <div class="files-main">
      <DiffView
        {provider}
        {platformHost}
        {owner}
        {name}
        {repoPath}
        {number}
        {diffHeadSHA}
        reviewDraftMutation={capabilities?.review_draft_mutation ?? false}
        supportedReviewActions={capabilities?.supported_review_actions ?? []}
        nativeMultilineRanges={capabilities?.native_multiline_ranges ?? false}
      />
    </div>
  </div>
</div>

<style>
  .files-view {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-height: 0;
    overflow: hidden;
  }

  .files-layout {
    display: flex;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .files-sidebar {
    width: var(--diff-file-tree-width, 280px);
    flex-shrink: 0;
    border-right: 1px solid var(--border-default);
    background: var(--bg-surface);
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }

  .files-main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  @media (max-width: 720px) {
    .files-layout {
      flex-direction: column;
    }

    .files-sidebar {
      width: 100%;
      max-height: 35vh;
      border-right: none;
      border-bottom: 1px solid var(--border-default);
    }

    :global(.files-resize-handle) {
      display: none;
    }

    .files-main {
      flex: 1;
      min-height: 0;
    }
  }
</style>
