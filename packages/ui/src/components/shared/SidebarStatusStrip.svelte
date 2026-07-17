<script lang="ts">
  import type { SidebarStatusStripTone } from "./sidebar-status-strip.js";

  interface Props {
    tone: SidebarStatusStripTone;
    label: string;
  }

  const { tone, label }: Props = $props();
</script>

<span
  class={["sidebar-status-strip", `sidebar-status-strip--${tone}`]}
  role="img"
  aria-label={label}
  title={label}
></span>

<style>
  .sidebar-status-strip {
    position: absolute;
    top: 50%;
    left: var(--sidebar-status-rail-left, 3px);
    display: block;
    width: 3px;
    height: 18px;
    transform: translateY(-50%);
    border-radius: 2px;
    background: var(--text-muted);
  }

  .sidebar-status-strip--success {
    background: var(--accent-green);
  }

  .sidebar-status-strip--warning {
    background: var(--accent-amber);
  }

  .sidebar-status-strip--danger {
    background: var(--accent-red);
  }

  .sidebar-status-strip--info {
    background: var(--accent-blue);
  }

  .sidebar-status-strip--merged {
    background: var(--accent-purple);
  }
</style>
