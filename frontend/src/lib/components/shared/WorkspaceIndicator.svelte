<script lang="ts">
  import MonitorUpIcon from "@lucide/svelte/icons/monitor-up";

  interface Props {
    status?: string | undefined;
    size?: number | undefined;
  }

  const { status = "", size = 13 }: Props = $props();

  const label = $derived(
    status ? `Workspace attached (${status})` : "Workspace attached",
  );
</script>

<span
  class="workspace-indicator"
  aria-label={label}
  title={label}
>
  <MonitorUpIcon {size} strokeWidth={2.2} aria-hidden="true" />
</span>

<style>
  .workspace-indicator {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
    color: var(--accent-teal, var(--accent-green));
  }
</style>
