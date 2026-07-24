<script lang="ts">
  import { tick, untrack, type Snippet } from "svelte";
  import { BottomDock, Button } from "@kenn-io/kit-ui";
  import ChevronsUpIcon from "@lucide/svelte/icons/chevrons-up";
  import type { InlineDockMode, InlineWorkspaceController } from "../../workspace-inline.js";

  interface Props {
    controller: InlineWorkspaceController;
    /** True while the surface's claim is active — gates the dock entirely. */
    active: boolean;
    /** The detail subtree; stays mounted (hidden + inert) while expanded. */
    children: Snippet;
  }

  let { controller, active, children }: Props = $props();

  const MIN_DOCK_PX = 160;
  const MAX_DOCK_RATIO = 0.8;
  const storageKey = $derived(`middleman-workspace-dock-height-${controller.surface}`);

  function clampDockHeight(px: number): number {
    const max = Math.round(window.innerHeight * MAX_DOCK_RATIO);
    return Math.max(MIN_DOCK_PX, Math.min(max, Math.round(px)));
  }

  function loadDockHeight(): number {
    try {
      const raw = localStorage.getItem(storageKey);
      const parsed = Number(raw);
      if (raw && Number.isFinite(parsed)) return clampDockHeight(parsed);
    } catch {
      /* Storage blocked */
    }
    return clampDockHeight(Math.round(window.innerHeight * 0.45));
  }

  function persistDockHeight(px: number): void {
    try {
      localStorage.setItem(storageKey, String(clampDockHeight(px)));
    } catch {
      /* Storage blocked */
    }
  }

  let dockHeightPx = $state(loadDockHeight());

  function handleHeightChange(next: string): void {
    const parsed = Number.parseFloat(next);
    if (!Number.isFinite(parsed)) return;
    dockHeightPx = clampDockHeight(parsed);
    persistDockHeight(dockHeightPx);
  }

  // Normalized re-persist after viewport changes so stale geometry cannot return.
  $effect(() => {
    const onResize = () => {
      const clamped = clampDockHeight(dockHeightPx);
      if (clamped !== dockHeightPx) {
        dockHeightPx = clamped;
        persistDockHeight(clamped);
      }
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  });

  const mode = $derived(active ? controller.getDockMode() : "collapsed");
  const expanded = $derived(active && mode === "expanded");
  const dockOpen = $derived(active && mode !== "collapsed");

  let detailWrapper = $state<HTMLElement | null>(null);
  let dockSlotEl = $state<HTMLElement | null>(null);

  // Focus restoration must never steal from a control the user moved to on
  // their own: a selection change resets an expanded dock to split, and by
  // the time the mode effect runs, focus belongs to the newly selected
  // sidebar row. Reclaim only when focus stayed inside the (closing)
  // terminal subtree or already fell to <body>.
  function shouldReclaimFocus(): boolean {
    const focused = document.activeElement;
    if (focused === null || focused === document.body) return true;
    return dockSlotEl?.contains(focused) ?? false;
  }

  // The expand/collapse controls live in the embedded WorkspaceTerminalView's
  // own toolbar, so track an expanded -> split transition and return focus
  // to the detail after it becomes visible. Like the reset-on-inactive effect
  // below, this runs inside an already-active flush, so the mode write that
  // drove it has resolved by the time this body runs; tick() keeps the focus
  // after the DOM update without relying on that scheduling detail.
  let lastObservedMode: InlineDockMode = untrack(() => mode);
  $effect(() => {
    const current = mode;
    const previous = lastObservedMode;
    lastObservedMode = current;
    if (active && previous === "expanded" && current === "split") {
      void tick().then(() => {
        if (shouldReclaimFocus()) detailWrapper?.focus();
      });
    }
  });

  // A claim that ends while expanded (deletion, stale release) must never leave
  // the detail hidden without a dock: reset to split and return focus to it.
  // Called from inside an $effect, which already runs as part of an active
  // flush, so the setDockMode write above resolves synchronously before this
  // line — tick() here is a defensive no-op that keeps the mechanism
  // consistent with collapseToDetail rather than relying on that Svelte
  // scheduling detail. Scheduling via tick().then(...) — not calling
  // flushSync (unsafe from within an already-running effect) or making this
  // effect itself async.
  $effect(() => {
    if (!active && controller.getDockMode() === "expanded") {
      controller.setDockMode("split");
      void tick().then(() => {
        if (shouldReclaimFocus()) detailWrapper?.focus();
      });
    }
  });

  // The reset above needs this panel mounted to observe active going
  // false. Navigation or selection clearing can unmount the panel
  // outright while expanded; the surface's mode lives on the controller
  // and would stay "expanded", so the next claimed item would open with
  // its detail hidden. Reset at teardown as well.
  $effect(() => {
    return () => {
      if (controller.getDockMode() === "expanded") {
        controller.setDockMode("split");
      }
    };
  });

  // Closing the dock outright — toolbar collapse to "collapsed", or the
  // claim releasing/deleting while split — unmounts the BottomDock subtree;
  // focus that lived inside the terminal falls to <body>, stranding
  // keyboard users and leaving global single-key shortcuts armed against
  // nothing. Reclaim it for the detail after the DOM update, but only when
  // focus actually fell to the body: a dock that closes in the background
  // while the user is typing elsewhere must not steal focus.
  let lastDockOpen = untrack(() => dockOpen);
  $effect(() => {
    const openNow = dockOpen;
    const wasOpen = lastDockOpen;
    lastDockOpen = openNow;
    if (wasOpen && !openNow) {
      void tick().then(() => {
        if (shouldReclaimFocus()) detailWrapper?.focus();
      });
    }
  });
</script>

<div class="workspace-dock-panel" class:workspace-dock-panel--expanded={expanded}>
  <div
    class="workspace-dock-detail"
    bind:this={detailWrapper}
    hidden={expanded}
    inert={expanded}
    tabindex="-1"
  >
    {@render children()}
  </div>

  {#if active && mode === "collapsed"}
    <!-- A collapsed dock must stay reachable from the pane itself, not
         only through the detail header's Focus Terminal action. -->
    <div class="workspace-dock-reopenstrip">
      <span class="workspace-dock-title">Workspace terminal</span>
      <Button
        size="sm"
        surface="soft"
        tone="neutral"
        label="Show terminal"
        onclick={() => controller.focusTerminal()}
      >
        <ChevronsUpIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      </Button>
    </div>
  {/if}

  {#if dockOpen}
    <BottomDock
      open={true}
      onclose={() => controller.setDockMode("collapsed")}
      ariaLabel="Workspace terminal"
      height={expanded ? "100%" : `${dockHeightPx}px`}
      minHeight={`${MIN_DOCK_PX}px`}
      maxHeight={expanded ? "100%" : "80vh"}
      onHeightChange={handleHeightChange}
      closable={false}
    >
      <div class="workspace-dock-slot" bind:this={dockSlotEl} {@attach controller.slotAttachment}></div>
    </BottomDock>
  {/if}
</div>

<style>
  .workspace-dock-panel {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    min-width: 0;
  }
  .workspace-dock-detail {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    min-width: 0;
    outline: none;
  }
  .workspace-dock-detail[hidden] {
    display: none;
  }
  .workspace-dock-slot {
    display: flex;
    flex: 1;
    min-height: 0;
    min-width: 0;
    height: 100%;
  }
  .workspace-dock-reopenstrip {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 6px 12px;
    border-top: 1px solid var(--border-muted);
    background: var(--bg-inset);
  }
  .workspace-dock-title {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
  }
  /* BottomDock has no prop to hide its top-edge resize handle. In expanded
   * mode the dock height is forced to 100% via the controlled `height` prop,
   * so a drag would not visibly resize anything — but it would still fire
   * onHeightChange and corrupt the persisted split-mode height. Hiding the
   * handle removes it from the tab order (display: none) as well as pointer
   * reach, so there is no dead/inert control left behind. Scoped to the
   * dock's own direct child: the hosted terminal reparents its split tree
   * into the dock body, and its nested pane handles must keep working
   * while expanded. */
  .workspace-dock-panel--expanded :global(.kit-bottom-dock > .kit-split-resize-handle) {
    display: none;
  }
</style>
