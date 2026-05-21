<script lang="ts">
  import type { Snippet } from "svelte";
  import LeftSidebarToggle from "./LeftSidebarToggle.svelte";
  import SplitResizeHandle from "./SplitResizeHandle.svelte";
  import type { SplitResizeEvent } from "./split-resize.js";

  interface Props {
    children?: Snippet | undefined;
    sidebar: Snippet;
    trailing?: Snippet | undefined;
    isCollapsed?: boolean;
    hideSidebar?: boolean;
    sidebarWidth?: number;
    sidebarOnly?: boolean;
    hasMain?: boolean;
    showCollapsedStrip?: boolean;
    mainEmpty?: boolean;
    mainOverflow?: "auto" | "hidden";
    minSidebarWidth?: number;
    maxSidebarWidth?: number;
    onSidebarResize?: ((width: number) => void) | undefined;
    onExpand?: (() => void) | undefined;
  }

  let {
    children = undefined,
    sidebar,
    trailing = undefined,
    isCollapsed = false,
    hideSidebar = false,
    sidebarWidth = 340,
    sidebarOnly = false,
    hasMain = true,
    showCollapsedStrip = false,
    mainEmpty = false,
    mainOverflow = "auto",
    minSidebarWidth = 200,
    maxSidebarWidth = 600,
    onSidebarResize = undefined,
    onExpand = undefined,
  }: Props = $props();

  // svelte-ignore state_referenced_locally
  // eslint-disable-next-line svelte/prefer-writable-derived -- $derived.writable not in svelte 5.55
  let committedWidth = $state(sidebarWidth);
  $effect(() => { committedWidth = sidebarWidth; });
  let dragWidth: number | null = $state(null);
  let currentWidth = $derived(dragWidth ?? committedWidth);
  let resizeStartWidth = 0;

  function handleResizeStart(): void {
    resizeStartWidth = currentWidth;
  }

  function widthFromResize(event: SplitResizeEvent): number {
    return Math.max(
      minSidebarWidth,
      Math.min(maxSidebarWidth, resizeStartWidth + event.deltaX),
    );
  }

  function handleResize(event: SplitResizeEvent): void {
    dragWidth = widthFromResize(event);
  }

  function handleResizeEnd(event: SplitResizeEvent): void {
    const finalWidth = widthFromResize(event);
    onSidebarResize?.(finalWidth);
    committedWidth = finalWidth;
    dragWidth = null;
  }
</script>

<div class="list-layout">
  {#if !isCollapsed && !hideSidebar}
    <aside
      class="sidebar"
      style={`width: ${sidebarOnly || !hasMain ? "100%" : `${currentWidth}px`}`}
    >
      {@render sidebar()}
    </aside>
    {#if !sidebarOnly && hasMain}
      <SplitResizeHandle
        ariaLabel="Resize sidebar"
        onResizeStart={handleResizeStart}
        onResize={handleResize}
        onResizeEnd={handleResizeEnd}
      />
    {/if}
  {:else if !hideSidebar && showCollapsedStrip}
    <aside class="sidebar sidebar--collapsed">
      <LeftSidebarToggle
        state="collapsed"
        label="sidebar"
        onclick={onExpand}
        class="left-sidebar-toggle--compact"
      />
    </aside>
  {/if}

  {#if hasMain}
    <section
      class="main-area"
      class:main-area--empty={mainEmpty}
      class:main-area--hidden={mainOverflow === "hidden"}
    >
      {#if children}
        {@render children()}
      {/if}
    </section>
  {/if}

  {#if trailing}
    {@render trailing()}
  {/if}
</div>

<style>
  .list-layout {
    display: flex;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .sidebar {
    width: 340px;
    flex-shrink: 0;
    background: var(--bg-surface);
    border-right: 1px solid var(--border-default);
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .sidebar--collapsed {
    width: 28px;
    align-items: center;
    padding-top: 6px;
  }

  .main-area {
    flex: 1;
    min-width: 0;
    min-height: 0;
    overflow-y: auto;
    background: var(--bg-primary);
    display: flex;
    flex-direction: column;
  }

  .main-area--empty {
    align-items: center;
    justify-content: center;
  }

  .main-area--hidden {
    overflow: hidden;
  }

  :global(#app.container-narrow) .list-layout {
    position: relative;
  }

  :global(#app.container-narrow) .sidebar:not(.sidebar--collapsed) {
    position: absolute;
    inset: 0 auto 0 0;
    z-index: 20;
    width: min(100%, 390px) !important;
    max-width: 100%;
    box-shadow: var(--shadow-lg);
  }

  :global(#app.container-narrow) .sidebar--collapsed {
    width: 36px;
    padding-top: 8px;
  }

  :global(#app.container-narrow) .main-area--empty {
    padding: 16px;
  }
</style>
