<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
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
    /** Names a tab from the render report, as the workspace pane's tab does. */
    labelFromRender?: boolean;
    /**
     * Forces a fresh `tabs` array without changing its contents. The real
     * surfaces re-derive their tab list from live stores, so its identity
     * changes on unrelated state — including as a consequence of a zoom.
     */
    tabsNonce?: number;
  }

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });

  const {
    layout,
    workspaceAvailable = true,
    routeTabKey = undefined,
    onSelectTab = undefined,
    onFocusPane = undefined,
    withLeafExtras = false,
    labelFromRender = false,
    tabsNonce = 0,
  }: Props = $props();

  const workspaceLabel = $derived(
    labelFromRender && layout.paneRender()?.flattened === false ? "Session" : "Workspace",
  );

  const tabs = $derived.by<PaneTabSpec[]>(() => {
    void tabsNonce;
    return [
      { key: "conversation", label: "Conversation", available: true },
      { key: "files", label: "Files", available: true },
      { key: "workspace", label: workspaceLabel, available: workspaceAvailable, hideable: true },
    ];
  });
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
  {#snippet renderPane(tabKey, visible, inputActive)}
    <section
      data-testid={`pane-${tabKey}`}
      data-visible={String(visible)}
      data-input-active={String(inputActive)}
    >
      Pane {tabKey}
      {#if visible}
        <button type="button" data-testid={`pane-focus-target-${tabKey}`}>Focus {tabKey}</button>
      {/if}
    </section>
  {/snippet}
</DetailPaneLayout>
