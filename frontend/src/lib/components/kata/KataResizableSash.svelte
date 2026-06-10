<script lang="ts">
  import type { Snippet } from "svelte";

  type Orientation = "vertical" | "horizontal";
  type SnippetFunction = () => ReturnType<Snippet>;

  interface Props {
    orientation: Orientation;
    primarySize: number;
    minPrimary?: number;
    minSecondary?: number;
    ariaLabel: string;
    onResize: (size: number) => void;
    primary: SnippetFunction;
    secondary: SnippetFunction;
  }

  let {
    orientation,
    primarySize,
    minPrimary = 200,
    minSecondary = 200,
    ariaLabel,
    onResize,
    primary,
    secondary,
  }: Props = $props();

  let container: HTMLDivElement | null = $state(null);
  let dragging = $state(false);
  let totalSize = $state(0);

  function axisSize(rect: DOMRect): number {
    return orientation === "vertical" ? rect.height : rect.width;
  }

  function clampSize(size: number, total: number): number {
    if (total <= 0) return Math.max(minPrimary, size);
    const maxPrimary = Math.max(minPrimary, total - minSecondary);
    return Math.max(minPrimary, Math.min(maxPrimary, size));
  }

  $effect(() => {
    if (!container) return;
    totalSize = axisSize(container.getBoundingClientRect());
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (!entry) return;
      totalSize = axisSize(entry.target.getBoundingClientRect());
    });
    observer.observe(container);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (totalSize <= 0) return;
    const clamped = clampSize(primarySize, totalSize);
    if (clamped !== primarySize) onResize(clamped);
  });

  function startDrag(event: PointerEvent): void {
    if (!container) return;
    event.preventDefault();
    const handle = event.currentTarget as HTMLElement;
    handle.setPointerCapture(event.pointerId);
    dragging = true;

    const rect = container.getBoundingClientRect();
    const total = axisSize(rect);
    const startPointer = orientation === "vertical" ? event.clientY : event.clientX;
    const startSize = primarySize;

    const move = (moveEvent: PointerEvent) => {
      const pointer = orientation === "vertical" ? moveEvent.clientY : moveEvent.clientX;
      onResize(clampSize(startSize + pointer - startPointer, total));
    };
    const release = (releaseEvent: PointerEvent) => {
      dragging = false;
      try {
        handle.releasePointerCapture(releaseEvent.pointerId);
      } catch {
        // Pointer capture may already be gone if the browser cancelled the drag.
      }
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", release);
      handle.removeEventListener("pointercancel", release);
    };

    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", release);
    handle.addEventListener("pointercancel", release);
  }

  function handleKeydown(event: KeyboardEvent): void {
    const step = event.shiftKey ? 64 : 16;
    let delta = 0;
    if (orientation === "vertical") {
      if (event.key === "ArrowUp") delta = -step;
      else if (event.key === "ArrowDown") delta = step;
    } else {
      if (event.key === "ArrowLeft") delta = -step;
      else if (event.key === "ArrowRight") delta = step;
    }
    if (delta === 0) return;
    event.preventDefault();
    if (!container) return;
    onResize(clampSize(primarySize + delta, axisSize(container.getBoundingClientRect())));
  }

  const appliedSize = $derived(totalSize > 0 ? clampSize(primarySize, totalSize) : primarySize);
  const valueMax = $derived(totalSize > 0 ? Math.max(minPrimary, totalSize - minSecondary) : minPrimary);
</script>

<div
  class={["kata-sash", `kata-sash--${orientation}`, dragging && "dragging"]}
  data-orientation={orientation}
  bind:this={container}
>
  <div class="pane pane-primary" style:flex-basis={`${Math.round(appliedSize)}px`}>
    {@render primary()}
  </div>
  <!-- The WAI-ARIA window-splitter pattern is interactive, but the linter treats
       div-with-tabindex as noninteractive by default. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="sash-handle"
    role="separator"
    aria-label={ariaLabel}
    aria-orientation={orientation === "vertical" ? "horizontal" : "vertical"}
    aria-valuemin={minPrimary}
    aria-valuemax={valueMax}
    aria-valuenow={Math.round(appliedSize)}
    tabindex="0"
    onpointerdown={startDrag}
    onkeydown={handleKeydown}
  ></div>
  <div class="pane pane-secondary">
    {@render secondary()}
  </div>
</div>

<style>
  .kata-sash {
    min-width: 0;
    min-height: 0;
    display: flex;
    flex: 1 1 auto;
    overflow: hidden;
  }

  .kata-sash--vertical {
    flex-direction: column;
  }

  .kata-sash--horizontal {
    flex-direction: row;
  }

  .pane {
    min-width: 0;
    min-height: 0;
    display: flex;
    overflow: hidden;
  }

  .pane-primary {
    flex: 0 0 auto;
  }

  .pane-secondary {
    flex: 1 1 auto;
  }

  .sash-handle {
    flex: 0 0 auto;
    border: 0;
    padding: 0;
    background: var(--border-default);
    appearance: none;
  }

  .kata-sash--horizontal .sash-handle {
    width: 4px;
    cursor: col-resize;
  }

  .kata-sash--vertical .sash-handle {
    height: 4px;
    cursor: row-resize;
  }

  .sash-handle:hover,
  .sash-handle:focus-visible,
  .dragging .sash-handle {
    background: var(--accent-blue);
    outline: none;
  }
</style>
