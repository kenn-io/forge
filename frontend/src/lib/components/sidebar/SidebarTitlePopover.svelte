<script lang="ts">
  import { autoReposition, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import { tick } from "svelte";

  interface Props {
    target: HTMLElement | undefined;
    title: string;
    repository: string;
    branch?: string | undefined;
    // Selector for the row elements whose ellipsis truncation this popover
    // reveals. The popover only opens while at least one match is actually
    // truncated, so untruncated rows never grow a redundant tooltip.
    truncationSelector: string;
  }

  let { target, title, repository, branch = undefined, truncationSelector }: Props = $props();

  const tooltipId = $props.id();
  // kit-ui-check-ignore: kit Tooltip neither portals to body nor describes the focused row button
  const tooltipRole = "tooltip";
  const hoverDelayMs = 600;

  let open = $state(false);
  let side = $state<"top" | "bottom">("bottom");
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
    popoverStyle = floatingPopoverStyle({
      trigger: target.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: popoverEl.offsetWidth,
      popoverHeight: popoverEl.offsetHeight,
      align: "start",
      triggerGap: 6,
    });
    const top = Number.parseFloat(/top: (-?[0-9.]+)px/.exec(popoverStyle)?.[1] ?? "0");
    side = top < target.getBoundingClientRect().top ? "top" : "bottom";
  }

  // Measured at show time (not mount) so resizes since render are honored.
  function hasTruncatedText(): boolean {
    if (!target) return false;
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
      if (!hasTruncatedText()) return;
      open = true;
      void position();
    }, hoverDelayMs);
  }

  function showNow(): void {
    clearTimer();
    if (open || !hasTruncatedText()) return;
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
    target.addEventListener("focusin", showNow);
    target.addEventListener("focusout", hide);
    target.addEventListener("keydown", handleKeydown);

    return () => {
      clearTimer();
      target.removeEventListener("mouseenter", showAfterDelay);
      target.removeEventListener("mouseleave", hide);
      target.removeEventListener("focusin", showNow);
      target.removeEventListener("focusout", hide);
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
  </div>
{/if}

<style>
  .sidebar-title-popover {
    position: fixed;
    z-index: var(--z-tooltip);
    width: max-content;
    max-width: min(420px, calc(100vw - 32px));
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

  .sidebar-title-popover::before {
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
