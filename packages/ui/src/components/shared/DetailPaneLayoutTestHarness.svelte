<script lang="ts">
  import DetailPaneLayout from "./DetailPaneLayout.svelte";
  import type { PaneLayoutStore, PaneTabSpec } from "../../stores/paneLayout.svelte.js";
  import type { TabbedPanelLeaf } from "./tabbed-panel-layout.js";

  interface Props {
    layout: PaneLayoutStore;
    workspaceAvailable?: boolean;
    routeTabKey?: string | undefined;
    onSelectTab?: ((tabKey: string) => void) | undefined;
    onFocusPane?: ((tabKey: string) => void) | undefined;
    /** Stands in for the app's workspace controls button. */
    withLeafExtras?: boolean;
  }

  const {
    layout,
    workspaceAvailable = true,
    routeTabKey = undefined,
    onSelectTab = undefined,
    onFocusPane = undefined,
    withLeafExtras = false,
  }: Props = $props();

  const tabs = $derived<PaneTabSpec[]>([
    { key: "conversation", label: "Conversation", available: true },
    { key: "files", label: "Files", available: true },
    { key: "workspace", label: "Workspace", available: workspaceAvailable, hideable: true },
  ]);
</script>

{#snippet leafExtras(leaf: TabbedPanelLeaf)}
  <button type="button" data-testid={`leaf-extra-${leaf.id}`}>Extra</button>
{/snippet}

<DetailPaneLayout
  {layout}
  {tabs}
  {routeTabKey}
  {onSelectTab}
  {onFocusPane}
  paneLeafExtras={withLeafExtras ? leafExtras : undefined}
>
  {#snippet renderPane(tabKey, visible)}
    <section data-testid={`pane-${tabKey}`} data-visible={String(visible)}>
      Pane {tabKey}
    </section>
  {/snippet}
</DetailPaneLayout>
