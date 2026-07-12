<script lang="ts">
  import { onDestroy, onMount, type Snippet } from "svelte";
  import type { ClassValue } from "svelte/elements";
  import { getScrollIndicatorGeometry } from "./sidebarScrollIndicator.js";

  interface Props {
    class?: ClassValue;
    dataTest?: string;
    label: string;
    children: Snippet;
  }

  const {
    class: className = "",
    dataTest,
    label,
    children,
  }: Props = $props();

  let viewportHeight = $state(0);
  let contentHeight = $state(0);
  let scrollTop = $state(0);
  let visible = $state(false);
  let hideTimer: number | undefined;
  let viewport: HTMLDivElement;
  let content: HTMLDivElement;

  const geometry = $derived(
    getScrollIndicatorGeometry(
      viewportHeight,
      contentHeight,
      scrollTop,
    ),
  );

  function handleScroll(event: Event): void {
    scrollTop = (event.currentTarget as HTMLDivElement).scrollTop;
    if (!geometry.scrollable) return;

    visible = true;
    window.clearTimeout(hideTimer);
    hideTimer = window.setTimeout(() => {
      visible = false;
    }, 700);
  }

  function updateDimensions(): void {
    viewportHeight = viewport.clientHeight;
    contentHeight = content.offsetHeight;
  }

  onMount(() => {
    updateDimensions();

    const observer = new ResizeObserver(updateDimensions);
    observer.observe(viewport);
    observer.observe(content);

    return () => observer.disconnect();
  });

  onDestroy(() => window.clearTimeout(hideTimer));
</script>

<div class={["sidebar-scroll-area", className]} data-test={dataTest}>
  <!-- Scrollable regions need keyboard access. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    class="sidebar-scroll-area__viewport"
    aria-label={label}
    bind:this={viewport}
    onscroll={handleScroll}
    role="region"
    tabindex="0"
  >
    <div
      class="sidebar-scroll-area__content"
      bind:this={content}
    >
      {@render children()}
    </div>
  </div>
  <div
    class={["sidebar-scroll-indicator", { visible: visible && geometry.scrollable }]}
    aria-hidden="true"
  >
    <span
      class="sidebar-scroll-indicator__thumb"
      style:height={`${geometry.height}px`}
      style:transform={`translateY(${geometry.top}px)`}
    ></span>
  </div>
</div>

<style>
  .sidebar-scroll-area {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .sidebar-scroll-area__viewport {
    height: 100%;
    overflow-y: auto;
    scrollbar-width: none;
  }

  .sidebar-scroll-area__viewport::-webkit-scrollbar {
    display: none;
    width: 0;
    height: 0;
  }

  .sidebar-scroll-area__content {
    min-height: 100%;
  }

  .sidebar-scroll-indicator {
    position: absolute;
    inset: 0 2px 0 auto;
    z-index: 2;
    width: 4px;
    pointer-events: none;
    opacity: 0;
    transition: opacity 160ms cubic-bezier(0.22, 1, 0.36, 1);
  }

  .sidebar-scroll-indicator.visible {
    opacity: 1;
  }

  .sidebar-scroll-indicator__thumb {
    display: block;
    width: 100%;
    border-radius: 999px;
    background: color-mix(in srgb, var(--text-muted) 72%, transparent);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--bg-primary) 35%, transparent);
    will-change: transform;
  }

  @media (prefers-reduced-motion: reduce) {
    .sidebar-scroll-indicator {
      transition: none;
    }
  }

  @media (forced-colors: active) {
    .sidebar-scroll-indicator__thumb {
      background: CanvasText;
      box-shadow: none;
    }
  }
</style>
