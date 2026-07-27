<script lang="ts">
  import SlidersHorizontalIcon from "@lucide/svelte/icons/sliders-horizontal";
  import { getStackDepth } from "@middleman/ui/stores/keyboard/modal-stack";
  import { workspaceControlsSnippet } from "../../stores/workspace-host.svelte.ts";

  // One button in a pane's tab strip, replacing the three bars that used to stack
  // above an embedded terminal. The contents come from the live view, which owns
  // every piece of state they act on.
  const controls = $derived(workspaceControlsSnippet());

  let open = $state(false);
  let rootEl = $state<HTMLDivElement | null>(null);

  // The host can go away under an open popover: the claim is released, the pane is
  // closed, the workspace is deleted. Nothing would then close it.
  $effect(() => {
    if (controls === null) open = false;
  });

  $effect(() => {
    if (!open) return;

    function onPointerDown(event: PointerEvent): void {
      if (rootEl && event.target instanceof Node && rootEl.contains(event.target)) return;
      open = false;
    }
    function onKeydown(event: KeyboardEvent): void {
      if (event.key !== "Escape") return;
      // A dialog opened from inside the popover (the font picker, a preset
      // prompt) owns Escape through the modal stack, and its handler runs after
      // this one, so the stack depth is the signal to stand down.
      if (getStackDepth() > 0) return;
      open = false;
    }
    window.addEventListener("pointerdown", onPointerDown, true);
    window.addEventListener("keydown", onKeydown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown, true);
      window.removeEventListener("keydown", onKeydown);
    };
  });
</script>

{#if controls}
  <div class="workspace-pane-controls" bind:this={rootEl}>
    <button
      class="controls-trigger"
      type="button"
      aria-label="Workspace controls"
      aria-haspopup="true"
      aria-expanded={open}
      title="Workspace controls"
      onclick={() => (open = !open)}
    >
      <SlidersHorizontalIcon size="13" strokeWidth="2" aria-hidden="true" />
    </button>
    {#if open}
      <div class="controls-popover" role="dialog" aria-label="Workspace controls">
        {@render controls()}
      </div>
    {/if}
  </div>
{/if}

<style>
  .workspace-pane-controls {
    position: relative;
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
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 30;
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
