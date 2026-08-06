<script lang="ts">
  import type { WorkspaceResources } from "../../api/types.js";

  interface Props {
    resources: WorkspaceResources | null;
  }

  let { resources }: Props = $props();
</script>

{#if resources}
  <footer class="resource-footer">
    <span class="stat">{resources.cpuPercent.toFixed(1)}% CPU</span>
    <span class="sep">&middot;</span>
    <span class="stat">{resources.residentMB}M RAM</span>
  </footer>
{/if}

<style>
  .resource-footer {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 0 10px;
    height: 28px;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    background: var(--bg-surface);
    border-top: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .stat {
    white-space: nowrap;
  }

  .sep {
    color: var(--text-muted);
  }
</style>
