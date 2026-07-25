<script lang="ts">
  import MaximizeIcon from "@lucide/svelte/icons/maximize";
  import MinimizeIcon from "@lucide/svelte/icons/minimize";
  import SquareSplitHorizontalIcon from "@lucide/svelte/icons/square-split-horizontal";
  import SquareSplitVerticalIcon from "@lucide/svelte/icons/square-split-vertical";
  import type { TabbedPanelDirection, TabbedPanelLeaf } from "./tabbed-panel-layout.js";

  interface Props {
    leaf: TabbedPanelLeaf;
    zoomed: boolean;
    onSplit: (
      tabKey: string,
      leafID: string,
      direction: TabbedPanelDirection,
      placement: "before" | "after",
    ) => void;
    onToggleZoom: (leafID: string) => void;
  }

  const { leaf, zoomed, onSplit, onToggleZoom }: Props = $props();

  // Splitting the only tab out of its own leaf is a no-op in the tree model, so
  // an enabled button here would be a dead control.
  const canSplit = $derived(leaf.tabs.length > 1);
</script>

<button
  class="tabbed-panel-tab-tool"
  type="button"
  title="Split right"
  aria-label="Split active pane right"
  disabled={!canSplit}
  data-testid="pane-split-right"
  onclick={() => onSplit(leaf.activeTabKey, leaf.id, "horizontal", "after")}
>
  <SquareSplitHorizontalIcon size="12" strokeWidth="2.2" aria-hidden="true" />
</button>

<button
  class="tabbed-panel-tab-tool"
  type="button"
  title="Split down"
  aria-label="Split active pane down"
  disabled={!canSplit}
  data-testid="pane-split-down"
  onclick={() => onSplit(leaf.activeTabKey, leaf.id, "vertical", "after")}
>
  <SquareSplitVerticalIcon size="12" strokeWidth="2.2" aria-hidden="true" />
</button>

<button
  class="tabbed-panel-tab-tool"
  type="button"
  title={zoomed ? "Restore" : "Maximize"}
  aria-label={zoomed ? "Restore pane size" : "Maximize pane"}
  aria-pressed={zoomed}
  data-testid="pane-toggle-zoom"
  onclick={() => onToggleZoom(leaf.id)}
>
  {#if zoomed}
    <MinimizeIcon size="12" strokeWidth="2.2" aria-hidden="true" />
  {:else}
    <MaximizeIcon size="12" strokeWidth="2.2" aria-hidden="true" />
  {/if}
</button>
