<script lang="ts">
  import DetailPaneLayout from "./DetailPaneLayout.svelte";
  import type { PaneLayoutStore, PaneTabSpec } from "../../stores/paneLayout.svelte.js";

  interface Props {
    layout: PaneLayoutStore;
    workspaceAvailable?: boolean;
    routeTabKey?: string | undefined;
    onSelectTab?: ((tabKey: string) => void) | undefined;
    onFocusPane?: ((tabKey: string) => void) | undefined;
  }

  const {
    layout,
    workspaceAvailable = true,
    routeTabKey = undefined,
    onSelectTab = undefined,
    onFocusPane = undefined,
  }: Props = $props();

  const tabs = $derived<PaneTabSpec[]>([
    { key: "conversation", label: "Conversation", available: true },
    { key: "files", label: "Files", available: true },
    { key: "workspace", label: "Workspace", available: workspaceAvailable, hideable: true },
  ]);
</script>

<DetailPaneLayout {layout} {tabs} {routeTabKey} {onSelectTab} {onFocusPane}>
  {#snippet renderPane(tabKey, visible)}
    <section data-testid={`pane-${tabKey}`} data-visible={String(visible)}>
      Pane {tabKey}
    </section>
  {/snippet}
</DetailPaneLayout>
