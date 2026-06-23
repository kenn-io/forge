<script lang="ts">
  import TabbedPanelTree from "./TabbedPanelTree.svelte";
  import type {
    TabbedPanelDescriptor,
    TabbedPanelDirection,
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
  }

  const {
    node,
    activeTabKey = "detail",
    onMoveTabBefore,
    onAppendTabToLeaf,
    onSplitTab,
    onRatioChange,
    disabled = false,
  }: Props = $props();

  const tabs: TabbedPanelDescriptor[] = [
    { key: "feed", label: "Feed", status: "running" },
    { key: "detail", label: "Detail" },
    { key: "files", label: "Files", status: "warning" },
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
  {onMoveTabBefore}
  {onAppendTabToLeaf}
  {onSplitTab}
  {onRatioChange}
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
