<script lang="ts">
  import SlidersHorizontalIcon from "@lucide/svelte/icons/sliders-horizontal";
  import { autoReposition, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import { getStackDepth } from "@middleman/ui/stores/keyboard/modal-stack";
  import { tick } from "svelte";
  import {
    hostedWorkspaceControls,
    workspaceControlsBusy,
  } from "../../stores/workspace-host.svelte.ts";

  // One button in a pane's tab strip, replacing the three bars that used to stack
  // above an embedded terminal. The contents come from the live view, which owns
  // every piece of state they act on.
  const controls = $derived(hostedWorkspaceControls());

  let open = $state(false);
  let triggerEl = $state<HTMLButtonElement | null>(null);
  let panelEl = $state<HTMLDivElement | null>(null);
  let panelStyle = $state("");
  // Which workspace the popover was opened for. The tab strip lives inside the
  // detail surface, which keeps rendering while the user selects a different item,
  // so the hosted workspace can change under an open popover.
  let openedFor = $state<string | null>(null);

  // Both cases leave a popover whose buttons no longer act on what the user opened
  // it for: the host went away (claim released, pane closed, workspace deleted), or
  // it now hosts a different workspace.
  $effect(() => {
    if (controls === null || controls.workspaceKey !== openedFor) close();
  });

  $effect(() => {
    if (!open) return;

    function onPointerDown(event: PointerEvent): void {
      if (triggerEl?.contains(event.target as Node) || panelEl?.contains(event.target as Node)) return;
      // A control with a write in flight owns its own pending feedback; dismissing
      // the popover under it would unmount that feedback mid-save.
      if (workspaceControlsBusy()) return;
      close();
    }
    function onKeydown(event: KeyboardEvent): void {
      if (event.key !== "Escape") return;
      // A dialog opened from inside the popover (the font picker, a preset prompt)
      // owns Escape through the modal stack, and its handler runs after this one,
      // so the stack depth is the signal to stand down.
      if (getStackDepth() > 0) return;
      if (workspaceControlsBusy()) return;
      close();
      triggerEl?.focus();
    }
    window.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("keydown", onKeydown);
    const stopRepositioning = autoReposition(() => panelEl, position);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("keydown", onKeydown);
      stopRepositioning();
    };
  });

  function close(): void {
    open = false;
    openedFor = null;
  }

  /**
   * Positioned against the viewport, not the strip.
   *
   * The tab strip scrolls horizontally and its leaf clips overflow, so an
   * absolutely positioned popover is cut off a few pixels below the trigger --
   * the controls would be there and unusable.
   */
  function position(): void {
    if (!triggerEl || !panelEl) return;
    panelStyle = floatingPopoverStyle({
      trigger: triggerEl.getBoundingClientRect(),
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      popoverWidth: panelEl.offsetWidth,
      popoverHeight: panelEl.offsetHeight,
      align: "end",
    });
  }

  /**
   * Rendered at the end of `<body>`, not where it is declared.
   *
   * The pane's leaf actions container is `position: relative; z-index: 2`, which
   * makes it a stacking context - so this popover's own z-index is clamped inside
   * it and loses to xterm's canvas layers, which compete one level up. Before the
   * terminal filled the pane that overlap never happened; now the popover paints
   * under the terminal and every click on it lands on the canvas instead. It is
   * already positioned against the viewport, so moving the node changes nothing
   * else.
   */
  function portalToBody(node: HTMLElement): () => void {
    document.body.appendChild(node);
    return () => node.remove();
  }

  async function toggle(): Promise<void> {
    if (open) {
      close();
      return;
    }
    open = true;
    openedFor = controls?.workspaceKey ?? null;
    // Twice, because the first measurement happens before the contents have laid
    // out and a popover placed against a zero-height panel picks the wrong side.
    await tick();
    position();
    await tick();
    position();
  }
</script>

{#if controls}
  <div class="workspace-pane-controls">
    <button
      bind:this={triggerEl}
      class="controls-trigger"
      type="button"
      aria-label="Workspace controls"
      aria-haspopup="true"
      aria-expanded={open}
      title="Workspace controls"
      onclick={() => void toggle()}
    >
      <SlidersHorizontalIcon size="13" strokeWidth="2" aria-hidden="true" />
    </button>
    {#if open}
      <div
        bind:this={panelEl}
        class="controls-popover"
        role="dialog"
        aria-label="Workspace controls"
        style={panelStyle}
        {@attach portalToBody}
      >
        {@render controls.snippet()}
      </div>
    {/if}
  </div>
{/if}

<style>
  .workspace-pane-controls {
    display: inline-flex;
  }

  .controls-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 20px;
    border: 1px solid transparent;
    border-radius: 3px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    transition:
      background-color 80ms ease,
      border-color 80ms ease,
      color 80ms ease;
  }

  .controls-trigger:hover,
  .controls-trigger[aria-expanded="true"] {
    background: var(--bg-surface-hover);
    border-color: var(--border-default);
    color: var(--text-primary);
  }

  .controls-popover {
    position: fixed;
    z-index: var(--z-popover);
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 6px;
    border: 1px solid var(--border-default);
    border-radius: 4px;
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
  }
</style>
