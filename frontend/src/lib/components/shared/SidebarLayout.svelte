<script lang="ts">
  // The kit sidebar layout with no fixed ceiling on the sidebar: it can be dragged
  // until only MIN_MAIN_WIDTH remains for the main area, as the Activity rail can.
  import { CollapsibleSidebar } from "@kenn-io/kit-ui";
  import type { ComponentProps, Snippet } from "svelte";

  type KitProps = ComponentProps<typeof CollapsibleSidebar>;

  interface Props extends Omit<KitProps, "sidebar" | "minSidebarWidth" | "maxSidebarWidth"> {
    /** Receives the width the sidebar actually has, which a narrow window can hold below the requested one. */
    sidebar: Snippet<[number]>;
  }

  const MIN_SIDEBAR_WIDTH = 200;
  // Kept for the main area so the sidebar cannot squeeze it, or its own resize handle, off screen.
  const MIN_MAIN_WIDTH = 360;
  // The reserve limits how far the sidebar grows. It never takes a sidebar below this width,
  // so a default sidebar in a narrow window stays as wide as it was before the limit was lifted.
  const DEFAULT_SIDEBAR_WIDTH = 340;

  const { sidebar: sidebarContent, sidebarWidth = DEFAULT_SIDEBAR_WIDTH, children, ...rest }: Props = $props();

  let layoutWidth = $state(0);
  // Before the layout is measured the requested width stands, so a reload never flashes a clamped sidebar.
  const maxSidebarWidth = $derived(
    layoutWidth > 0 ? Math.max(DEFAULT_SIDEBAR_WIDTH, layoutWidth - MIN_MAIN_WIDTH) : Math.max(MIN_SIDEBAR_WIDTH, sidebarWidth),
  );
  const width = $derived(Math.min(sidebarWidth, maxSidebarWidth));
</script>

<div class="sidebar-layout" bind:clientWidth={layoutWidth}>
  <CollapsibleSidebar {...rest} sidebarWidth={width} minSidebarWidth={MIN_SIDEBAR_WIDTH} {maxSidebarWidth} {children}>
    {#snippet sidebar()}
      {@render sidebarContent(width)}
    {/snippet}
  </CollapsibleSidebar>
</div>

<style>
  .sidebar-layout {
    display: flex;
    flex: 1;
    min-width: 0;
    min-height: 0;
  }
</style>
