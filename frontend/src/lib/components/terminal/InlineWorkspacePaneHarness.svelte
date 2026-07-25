<script lang="ts">
  import DetailPaneLayout from "../../../../../packages/ui/src/components/shared/DetailPaneLayout.svelte";
  import type {
    PaneLayoutStore,
    PaneTabSpec,
  } from "../../../../../packages/ui/src/stores/paneLayout.svelte.js";
  import type { InlineWorkspaceController } from "@middleman/ui";

  interface Props {
    layout: PaneLayoutStore;
    workspaceAvailable?: boolean;
    /**
     * When given, the workspace pane renders the real portal slot instead of a
     * stand-in control, so a spec can exercise live terminal reparenting.
     */
    controller?: InlineWorkspaceController | null;
  }

  const { layout, workspaceAvailable = true, controller = null }: Props = $props();

  const tabs = $derived<PaneTabSpec[]>([
    { key: "conversation", label: "Conversation", available: true },
    { key: "workspace", label: "Workspace", available: workspaceAvailable, hideable: true },
  ]);
</script>

<div class="harness-root">
  <DetailPaneLayout {layout} {tabs}>
    {#snippet renderPane(tabKey, visible)}
      {#if tabKey === "workspace" && visible && controller}
        <div class="harness-workspace-slot" {@attach controller.slotAttachment}></div>
      {:else if tabKey === "workspace" && visible}
        <!-- A real focusable control, standing in for the terminal subtree that
             the workspace host reparents into this slot in production. -->
        <button type="button">terminal</button>
      {:else if tabKey === "conversation"}
        <p>conversation</p>
      {/if}
    {/snippet}
  </DetailPaneLayout>
</div>

<style>
  .harness-root {
    display: flex;
    width: 100%;
    height: 100%;
  }

  .harness-workspace-slot {
    display: flex;
    flex: 1;
    min-width: 0;
    min-height: 0;
    height: 100%;
  }
</style>
