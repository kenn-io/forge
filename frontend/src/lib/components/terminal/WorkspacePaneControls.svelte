<script lang="ts">
  import SlidersHorizontalIcon from "@lucide/svelte/icons/sliders-horizontal";
  import { autoReposition, floatingPopoverStyle } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { getStackDepth } from "../../stores/keyboard/modal-stack.svelte.js";
  import { tick } from "svelte";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import {
    hostedWorkspaceControls,
    workspaceControlsBusy,
  } from "../../stores/workspace-host.svelte.ts";

  interface Props {
    /**
     * Whether this instance renders the owner-only strip actions (Delete). One
     * popover per related leaf is fine - it acts on the workspace wherever it
     * opens - but the owner-only actions are visible destructive controls, and a
     * workspace split across leaves (its pane in one, a promoted session in
     * another) must not grow one Delete per leaf. The surface passes true only for
     * the leaf holding the workspace pane itself.
     */
    showStripActions?: boolean;
  }

  const { showStripActions = true }: Props = $props();
  const runtime = getAppRuntime();

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
  let placementExecution: AppExecution<void, never> | null = null;

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
    placementExecution?.interrupt();
    placementExecution = null;
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

  function toggle(): void {
    if (open) {
      close();
      return;
    }
    open = true;
    openedFor = controls?.workspaceKey ?? null;
    // Twice, because the first measurement happens before the contents have laid
    // out and a popover placed against a zero-height panel picks the wrong side.
    placementExecution = runtime.runCommand(
      Effect.gen(function* () {
        yield* Effect.promise(() => tick());
        position();
        yield* Effect.promise(() => tick());
        position();
      }),
      {
        operation: "position workspace controls",
        safeContext: { workspaceKey: openedFor ?? "" },
        onFailure: () => {},
      },
    );
  }
</script>

{#if controls}
  <div class="workspace-pane-controls">
    {@render controls.paneActions?.()}
    {#if showStripActions}
      {@render controls.stripActions?.()}
    {/if}
    <button
      bind:this={triggerEl}
      class="controls-trigger"
      type="button"
      aria-label="Workspace controls"
      aria-haspopup="true"
      aria-expanded={open}
      title="Workspace controls"
      onclick={toggle}
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
    align-items: center;
    gap: 2px;
  }

  .controls-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    /* kit IconButton sm geometry, because the Delete rendered beside this trigger
       is one: the row reads as one set only if every member shares the box. */
    width: 24px;
    height: 24px;
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
    /*
     * Below the modal layer, not level with it. This popover's own controls open
     * modals (rename, stop, the font picker) and it deliberately stays open
     * underneath them, but portalling put it after every in-tree modal in document
     * order -- so at an equal z-index it painted over the dialog it had just
     * opened and swallowed the clicks meant for it.
     */
    z-index: calc(var(--z-overlay) - 1);
    /*
     * Wraps rather than growing. The contents are whatever the hosted view hands
     * over -- with a workspace running an agent that is two dock modes, zoom, the
     * options menu, rename, stop, the branch, Delete and Launch -- and in one row
     * that is a bar the width of the pane, which is the stacked chrome this popover
     * replaced. A few short rows read as a menu instead.
     */
    max-width: min(440px, calc(100vw - 24px));
    /* Explicit, not inherited from a global reset: this is portalled to <body>,
       and the cap has to mean the whole box or padding and border push it past
       the width it was capped to. */
    box-sizing: border-box;
    /*
     * A branch name is one unbreakable word, and a long one sets a min-content
     * width the cap above cannot beat - the popover grew past 440px and back to
     * the pane-wide bar this replaced. `anywhere` (not `break-word`) is what
     * actually lowers min-content.
     */
    overflow-wrap: anywhere;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 4px;
    padding: 6px;
    border: 1px solid var(--border-default);
    border-radius: 4px;
    background: var(--bg-surface);
    box-shadow: var(--shadow-lg);
  }
</style>
