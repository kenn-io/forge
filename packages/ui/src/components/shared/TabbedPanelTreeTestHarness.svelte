<script lang="ts">
  import TabbedPanelTree from "./TabbedPanelTree.svelte";
  import type {
    TabbedPanelDescriptor,
    TabbedPanelDirection,
    TabbedPanelLeaf,
    TabbedPanelNode,
  } from "./tabbed-panel-layout.js";

  interface Props {
    node: TabbedPanelNode;
    activeTabKey?: string;
    onMoveTabBefore?: ((sourceTabKey: string, targetTabKey: string) => void) | undefined;
    onAppendTabToLeaf?: ((sourceTabKey: string, leafID: string) => void) | undefined;
    onSplitTab?:
      | ((
          sourceTabKey: string,
          leafID: string,
          direction: TabbedPanelDirection,
          placement: "before" | "after",
        ) => void)
      | undefined;
    onRatioChange?: ((splitID: string, ratio: number) => void) | undefined;
    disabled?: boolean;
    zoomedLeafID?: string | null;
    /** Render the per-leaf action cluster; off by default so existing cases are unaffected. */
    withLeafActions?: boolean;
    onLeafAction?: ((leafID: string) => void) | undefined;
  }

  const {
    node,
    activeTabKey = "detail",
    onMoveTabBefore,
    onAppendTabToLeaf,
    onSplitTab,
    onRatioChange,
    disabled = false,
    zoomedLeafID = null,
    withLeafActions = false,
    onLeafAction = undefined,
  }: Props = $props();

  const tabs: TabbedPanelDescriptor[] = [
    {
      key: "feed",
      label: "Feed",
      status: { value: "working", label: "Feed updating" },
    },
    { key: "detail", label: "Detail" },
    {
      key: "files",
      label: "Files",
      status: { value: "unclean", label: "Files need attention" },
    },
  ];
</script>

<TabbedPanelTree
  dragScope="test-workspace"
  {node}
  {tabs}
  {activeTabKey}
  tablistLabel="Test panel tabs"
  leafLabel="Test panel group"
  dropTargetsLabel="Test panel drop targets"
  resizeLabel="Resize test split"
  {disabled}
  {zoomedLeafID}
  {onMoveTabBefore}
  {onAppendTabToLeaf}
  {onSplitTab}
  {onRatioChange}
  leafActions={withLeafActions ? leafActions : undefined}
>
  {#snippet renderTab(tabKey, active)}
    <section data-testid={`panel-${tabKey}`} data-active={String(active)}>
      Panel {tabKey}
    </section>
  {/snippet}

  {#snippet tabIcon(tab)}
    <span data-testid={`icon-${tab.key}`}>i</span>
  {/snippet}

  {#snippet tabActions(tab)}
    <button class="tabbed-panel-tab-tool" type="button" aria-label={`Action ${tab.label}`}>
      A
    </button>
  {/snippet}
</TabbedPanelTree>

{#snippet leafActions(leaf: TabbedPanelLeaf)}
  <button
    class="tabbed-panel-tab-tool"
    type="button"
    data-testid={`leaf-action-${leaf.id}`}
    aria-label={`Leaf action ${leaf.id}`}
    disabled={leaf.tabs.length <= 1}
    onclick={() => onLeafAction?.(leaf.id)}
  >
    L
  </button>
{/snippet}
