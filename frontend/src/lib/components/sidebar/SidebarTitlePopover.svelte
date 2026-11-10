<script lang="ts">
  import { autoReposition, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import { tick, type Snippet } from "svelte";

  interface Props {
    target: HTMLElement | undefined;
    title: string;
    repository: string;
    branch?: string | undefined;
    // Selector for the row elements whose ellipsis truncation this popover
    // reveals. The popover only opens while at least one match is actually
    // truncated, so untruncated rows never grow a redundant tooltip.
    truncationSelector: string;
    // Extra lines under the branch. A row that passes them has more to say
    // than its own text, so the popover opens whether or not it is truncated.
    details?: Snippet | undefined;
  }

  let { target, title, repository, branch = undefined, truncationSelector, details = undefined }: Props = $props();

  const tooltipId = $props.id();
  // kit-ui-check-ignore: kit Tooltip neither portals to body nor describes the focused row button
  const tooltipRole = "tooltip";
  const hoverDelayMs = 600;

  let open = $state(false);
  let side = $state<"right" | "left" | "top" | "bottom">("right");
  const edgeGap = 8;
  const sideGap = 6;

  let popoverEl = $state<HTMLDivElement>();
  let popoverStyle = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;

  function clearTimer(): void {
    if (timer === undefined) return;
    clearTimeout(timer);
    timer = undefined;
  }

  async function position(): Promise<void> {
    await tick();
    if (!target || !popoverEl) return;
    const row = target.getBoundingClientRect();
    const width = popoverEl.offsetWidth;
    const height = popoverEl.offsetHeight;
    // Beside the row, so the list stays readable: right first, then left.
    const right = row.right + sideGap;
    const left = row.left - sideGap - width;
    const fitsRight = right + width <= window.innerWidth - edgeGap;
    if (fitsRight || left >= edgeGap) {
      // Top edges line up; without room below the row, bottom edges do.
      const top = row.top + height <= window.innerHeight - edgeGap ? row.top : row.bottom - height;
      popoverStyle = `left: ${Math.round(fitsRight ? right : left)}px; top: ${Math.round(Math.max(edgeGap, top))}px`;
      side = fitsRight ? "right" : "left";
      return;
    }
    // A row as wide as the viewport has no side; open below or above it.
    popoverStyle = floatingPopoverStyle({
      trigger: row,
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: width,
      popoverHeight: height,
      align: "start",
      triggerGap: sideGap,
    });
    const top = Number.parseFloat(/top: (-?[0-9.]+)px/.exec(popoverStyle)?.[1] ?? "0");
    side = top < row.top ? "top" : "bottom";
  }

  // Measured at show time (not mount) so resizes since render are honored.
  function hasContent(): boolean {
    if (!target) return false;
    if (details) return true;
    for (const el of target.querySelectorAll(truncationSelector)) {
      if (el.scrollWidth > el.clientWidth) return true;
    }
    return false;
  }

  function showAfterDelay(): void {
    clearTimer();
    if (open) return;
    timer = setTimeout(() => {
      timer = undefined;
      if (!hasContent()) return;
      open = true;
      void position();
    }, hoverDelayMs);
  }

  // Keyboard focus opens the popover at once. Focus from a click, or from
  // code after one, does not: the user chose the row and is looking past it.
  function showOnFocus(event: FocusEvent): void {
    clearTimer();
    if (!(event.target instanceof Element) || !event.target.matches(":focus-visible")) return;
    if (open || !hasContent()) return;
    open = true;
    void position();
  }

  function hide(): void {
    clearTimer();
    open = false;
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") hide();
  }

  function portalToBody(node: HTMLElement): () => void {
    document.body.appendChild(node);
    return () => node.remove();
  }

  $effect(() => {
    if (!target) return;

    target.addEventListener("mouseenter", showAfterDelay);
    target.addEventListener("mouseleave", hide);
    target.addEventListener("focusin", showOnFocus);
    target.addEventListener("focusout", hide);
    target.addEventListener("mousedown", hide);
    target.addEventListener("keydown", handleKeydown);

    return () => {
      clearTimer();
      target.removeEventListener("mouseenter", showAfterDelay);
      target.removeEventListener("mouseleave", hide);
      target.removeEventListener("focusin", showOnFocus);
      target.removeEventListener("focusout", hide);
      target.removeEventListener("mousedown", hide);
      target.removeEventListener("keydown", handleKeydown);
    };
  });

  $effect(() => {
    if (!open || !target) return;
    target.setAttribute("aria-describedby", tooltipId);
    const stopRepositioning = autoReposition(
      () => [popoverEl],
      () => void position(),
    );
    return () => {
      stopRepositioning();
      if (target.getAttribute("aria-describedby") === tooltipId) {
        target.removeAttribute("aria-describedby");
      }
    };
  });
</script>

{#if open}
  <div
    bind:this={popoverEl}
    id={tooltipId}
    class="sidebar-title-popover kit-popover-card"
    role={tooltipRole}
    data-side={side}
    style={popoverStyle}
    {@attach portalToBody}
  >
    <div class="sidebar-title-popover__title">{title}</div>
    <div class="sidebar-title-popover__metadata">{repository}</div>
    {#if branch}
      <div class="sidebar-title-popover__metadata sidebar-title-popover__branch">{branch}</div>
    {/if}
    {@render details?.()}
  </div>
{/if}

<style>
  .sidebar-title-popover {
    position: fixed;
    z-index: var(--z-tooltip);
    width: max-content;
    max-width: min(420px, calc(100vw - 32px));
    max-height: calc(100dvh - 16px);
    padding: var(--space-4) var(--space-5);
    display: grid;
    gap: var(--space-2);
    font-size: var(--font-size-sm);
    line-height: 1.4;
    overflow-wrap: anywhere;
    pointer-events: none;
  }

  .sidebar-title-popover__title {
    color: var(--text-primary);
    font-weight: 500;
  }

  .sidebar-title-popover__metadata {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    line-height: 1.35;
  }

  .sidebar-title-popover__branch {
    font-family: "SFMono-Regular", "Consolas", "Liberation Mono", "Menlo", monospace;
  }

  /* Beside the row there is no arrow to clip, so a card taller than the
     viewport cuts off at its edge. */
  .sidebar-title-popover[data-side="right"],
  .sidebar-title-popover[data-side="left"] {
    overflow: hidden;
  }

  .sidebar-title-popover[data-side="bottom"]::before,
  .sidebar-title-popover[data-side="top"]::before {
    content: "";
    position: absolute;
    left: 18px;
    width: 8px;
    height: 8px;
    transform: rotate(45deg);
    background: var(--bg-surface);
  }

  .sidebar-title-popover[data-side="bottom"]::before {
    top: -5px;
    border-top: 1px solid var(--border-default);
    border-left: 1px solid var(--border-default);
  }

  .sidebar-title-popover[data-side="top"]::before {
    bottom: -5px;
    border-right: 1px solid var(--border-default);
    border-bottom: 1px solid var(--border-default);
  }
</style>
