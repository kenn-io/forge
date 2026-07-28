<script lang="ts">
  import MaximizeIcon from "@lucide/svelte/icons/maximize";
  import MinimizeIcon from "@lucide/svelte/icons/minimize";
  import type { TabbedPanelLeaf } from "./tabbed-panel-layout.js";

  interface Props {
    leaf: TabbedPanelLeaf;
    zoomed: boolean;
    onToggleZoom: (leafID: string) => void;
  }

  const { leaf, zoomed, onToggleZoom }: Props = $props();
</script>

<!--
  Zoom only. Split right and Split down used to sit here and split the leaf's ACTIVE
  tab out of its own leaf, which is a no-op when the leaf holds one tab - so on the
  panes that need splitting least they were two permanently greyed controls, and on
  the rest they duplicated dragging a tab to a pane edge, which is how splitting
  actually gets done.
-->
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
