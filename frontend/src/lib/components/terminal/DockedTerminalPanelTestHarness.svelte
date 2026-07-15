<script lang="ts">
  import DockedTerminalPanel from "./DockedTerminalPanel.svelte";

  interface Props {
    height?: number;
    disabled?: boolean;
    onResize?: (height: number) => void;
  }

  let { height = 300, disabled = false, onResize = () => {} }: Props = $props();
</script>

<DockedTerminalPanel
  workspaceId="ws-1"
  sessions={[]}
  displayLabels={{}}
  tree={null}
  activeSessionKey={null}
  open={true}
  dock="bottom"
  {height}
  {disabled}
  {onResize}
/>
