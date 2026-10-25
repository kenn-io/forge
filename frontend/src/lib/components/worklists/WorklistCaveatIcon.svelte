<script lang="ts">
  import AlertTriangleIcon from "@lucide/svelte/icons/alert-triangle";
  import InfoIcon from "@lucide/svelte/icons/info";
  import OctagonAlertIcon from "@lucide/svelte/icons/octagon-alert";
  import type { WorklistItem } from "../../api/worklists.js";

  interface Props {
    level: WorklistItem["caveat_level"];
    size: number;
  }

  let { level, size }: Props = $props();
</script>

<!-- Only warnings and critical caveats announce a level; info reads as plain context. -->
<span class={["worklist-caveat-icon", `level-${level}`]}>
  {#if level === "critical"}
    <span class="kit-sr-only">Critical:</span>
    <OctagonAlertIcon {size} aria-hidden="true" />
  {:else if level === "warning"}
    <span class="kit-sr-only">Warning:</span>
    <AlertTriangleIcon {size} aria-hidden="true" />
  {:else}
    <InfoIcon {size} aria-hidden="true" />
  {/if}
</span>

<style>
  .worklist-caveat-icon {
    display: inline-flex;
    flex-shrink: 0;
    color: var(--text-muted);
  }

  .level-warning {
    color: var(--accent-amber);
  }

  .level-critical {
    color: var(--accent-red);
  }
</style>
