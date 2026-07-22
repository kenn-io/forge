<script lang="ts">
  import { flushSync, tick } from "svelte";
  import type { InlineDockMode } from "@middleman/ui";
  import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";
  import { getRoute } from "../../stores/router.svelte.ts";
  import {
    clearPendingHostFocus, consumePendingHostFocus,
    desiredKey, desiredSlot, getInlineWorkspaceController, getSlotElement, isHostVisible,
    notifyWorkspaceDeleted, registerHostElement, registerParkingElement,
    rememberTerminalRouteKey,
    type HostSlot,
  } from "../../stores/workspace-host.svelte.ts";

  interface Props {
    isSidebarCollapsed?: boolean;
    sidebarWidth?: number;
    onSidebarResize?: (width: number) => void;
    isSidebarToggleEnabled?: boolean;
    onToggleSidebar?: () => void;
  }

  let {
    isSidebarCollapsed = false,
    sidebarWidth = undefined,
    onSidebarResize = undefined,
    isSidebarToggleEnabled = false,
    onToggleSidebar = undefined,
  }: Props = $props();

  let hostWrapper = $state<HTMLElement | null>(null);
  let parkingNode = $state<HTMLElement | null>(null);
  // Revealed only after the wrapper sits in its destination with non-zero
  // geometry; combined with the store's visibility (dock collapse, parked).
  let attachedVisible = $state(false);

  const slot = $derived(desiredSlot());
  const key = $derived(desiredKey());
  const embedded = $derived(slot !== null && slot !== "tab");
  const visible = $derived(attachedVisible && isHostVisible());

  function inlineDockForSlot(
    current: HostSlot | null,
  ): { getMode(): InlineDockMode; setMode(mode: InlineDockMode): void } | null {
    if (current === null || current === "tab") return null;
    const controller = getInlineWorkspaceController(current);
    return {
      getMode: () => controller.getDockMode(),
      setMode: (mode) => controller.setDockMode(mode),
    };
  }

  // Backs the toolbar's expand/show-details/collapse controls in WTV; only
  // set while this host sits in an inline dock slot (activity/prs/issues),
  // never for the Workspaces tab or the parked (slot === null) case.
  const inlineDock = $derived(inlineDockForSlot(slot));

  $effect(() => {
    registerHostElement(hostWrapper);
    registerParkingElement(parkingNode);
    return () => {
      registerHostElement(null);
      registerParkingElement(null);
    };
  });

  // Keep the inline-fallback key fresh while /terminal/{id} is the active
  // route. desiredKey() falls back to this value once the route leaves
  // "terminal", so it must already hold the terminal's key by the time that
  // happens — otherwise it briefly resolves to the stale initial empty key
  // during the async gap before a surface's own claim effect resolves,
  // which drops workspaceId to "" and tears down the live terminal for a
  // moment (a spurious reconnect on the very first inline claim of a
  // workspace already open in the tab).
  $effect(() => {
    const route = getRoute();
    if (route.page === "terminal") {
      rememberTerminalRouteKey({ workspaceId: route.workspaceId, hostKey: route.hostKey });
    }
  });

  // Placement: hide first, park, attach to the destination, then reveal once
  // geometry is non-zero (terminal panes resize themselves on activation).
  $effect(() => {
    const destination = slot === null ? parkingNode : getSlotElement(slot);
    const wrapper = hostWrapper;
    const parking = parkingNode;
    if (!wrapper || !parking) return;
    attachedVisible = false;
    parking.appendChild(wrapper);
    if (!destination || destination === parking) {
      // Parking: a focus request queued before this park (e.g. a dock
      // collapsed again before its expand ever revealed) must not fire on
      // some later, unrelated reveal.
      clearPendingHostFocus();
      return;
    }
    let cancelled = false;
    void (async () => {
      await tick();
      if (cancelled) return;
      destination.appendChild(wrapper);
      const reveal = () => {
        if (cancelled) return;
        if (wrapper.getBoundingClientRect().height > 0) {
          const shouldFocus = consumePendingHostFocus();
          // flushSync (safe here: this callback runs outside any active
          // Svelte effect) forces the `inert` attribute removal to land in
          // the DOM before focus() is attempted — a real browser silently
          // ignores focus() on a still-inert element, and the plain
          // assignment below only schedules that DOM update for the next
          // microtask.
          flushSync(() => {
            attachedVisible = true;
          });
          if (shouldFocus) wrapper.focus();
          return;
        }
        requestAnimationFrame(reveal);
      };
      requestAnimationFrame(reveal);
    })();
    return () => {
      cancelled = true;
    };
  });
</script>

<div class="workspace-host-parking" bind:this={parkingNode} aria-hidden="true"></div>

<div
  class="workspace-host-wrapper"
  bind:this={hostWrapper}
  inert={!visible}
  tabindex="-1"
>
  <!-- hideWorkspaceList and hideRightSidebar also cover the parked case
       (slot === null), not only embedded: both unmount their subtree
       rather than merely hiding it, so a parked host (sitting in the
       display:none parking node, possibly for the rest of the session)
       doesn't keep WorkspaceListSidebar's 5s poll, fleet-status poll, and
       EventSource — or the right sidebar's diff panel and its listeners —
       running forever on every unrelated page. Sidebar open state and
       width persist in the store/localStorage, so re-revealing restores
       them. The terminal main subtree this wrapper hosts is unaffected by
       those toggles, so the workspace's own sockets stay warm. -->
  <WorkspaceTerminalView
    workspaceId={key.workspaceId}
    workspaceHostKey={key.hostKey}
    hideWorkspaceList={embedded || slot === null}
    hideRightSidebar={embedded || slot === null}
    hostVisible={visible}
    onWorkspaceDeleted={notifyWorkspaceDeleted}
    isSidebarCollapsed={embedded ? false : isSidebarCollapsed}
    sidebarWidth={embedded ? undefined : sidebarWidth}
    onSidebarResize={embedded ? undefined : onSidebarResize}
    isSidebarToggleEnabled={embedded ? false : isSidebarToggleEnabled}
    onToggleSidebar={embedded ? undefined : onToggleSidebar}
    {inlineDock}
  />
</div>

<style>
  .workspace-host-parking {
    display: none;
  }
  .workspace-host-wrapper {
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    min-height: 0;
    outline: none;
  }
</style>
