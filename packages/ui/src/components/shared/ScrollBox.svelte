<script lang="ts">
  import { onDestroy, onMount, type Snippet } from "svelte";
  import type { ClassValue, HTMLAttributes } from "svelte/elements";
  import { getScrollIndicatorGeometry } from "./scrollIndicator.js";

  interface Props extends Omit<HTMLAttributes<HTMLDivElement>, "class" | "onscroll"> {
    class?: ClassValue;
    dataTest?: string | undefined;
    label: string;
    onscroll?: ((event: Event) => void) | undefined;
    viewport?: HTMLDivElement | undefined;
    children: Snippet;
  }

  let {
    class: className = "",
    dataTest,
    label,
    onscroll,
    viewport = $bindable(),
    children,
    ...rest
  }: Props = $props();

  let viewportHeight = $state(0);
  let contentHeight = $state(0);
  let scrollTop = $state(0);
  let visible = $state(false);
  let hideTimer: number | undefined;
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
    if (geometry.scrollable) {
      visible = true;
      window.clearTimeout(hideTimer);
      hideTimer = window.setTimeout(() => {
        visible = false;
      }, 700);
    }
    onscroll?.(event);
  }

  function updateDimensions(): void {
    if (!viewport) return;
    viewportHeight = viewport.clientHeight;
    contentHeight = content.offsetHeight;
  }

  onMount(() => {
    updateDimensions();

    const observer = new ResizeObserver(updateDimensions);
    if (viewport) observer.observe(viewport);
    observer.observe(content);

    return () => observer.disconnect();
  });

  onDestroy(() => window.clearTimeout(hideTimer));
</script>

<div class={["scroll-box", className]} data-test={dataTest}>
  <!-- Scrollable regions need keyboard access. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    tabindex="0"
    {...rest}
    class="scroll-box__viewport"
    aria-label={label}
    bind:this={viewport}
    onscroll={handleScroll}
    role="region"
  >
    <div
      class="scroll-box__content"
      bind:this={content}
    >
      {@render children()}
    </div>
  </div>
  <div
    class={["scroll-box__indicator", { visible: visible && geometry.scrollable }]}
    aria-hidden="true"
  >
    <span
      class="scroll-box__thumb"
      style:height={`${geometry.height}px`}
      style:transform={`translateY(${geometry.top}px)`}
    ></span>
  </div>
</div>

<style>
  .scroll-box {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .scroll-box__viewport {
    height: 100%;
    overflow-y: auto;
    scrollbar-width: none;
  }

  .scroll-box__viewport::-webkit-scrollbar {
    display: none;
    width: 0;
    height: 0;
  }

  .scroll-box__content {
    min-height: 100%;
  }

  .scroll-box__indicator {
    position: absolute;
    inset: 0 2px 0 auto;
    z-index: 2;
    width: 4px;
    pointer-events: none;
    opacity: 0;
    transition: opacity 160ms cubic-bezier(0.22, 1, 0.36, 1);
  }

  .scroll-box__indicator.visible {
    opacity: 1;
  }

  .scroll-box__thumb {
    display: block;
    width: 100%;
    border-radius: 999px;
    background: color-mix(in srgb, var(--text-muted) 72%, transparent);
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--bg-primary) 35%, transparent);
    will-change: transform;
  }

  @media (prefers-reduced-motion: reduce) {
    .scroll-box__indicator {
      transition: none;
    }
  }

  @media (forced-colors: active) {
    .scroll-box__thumb {
      background: CanvasText;
      box-shadow: none;
    }
  }
</style>
