<script lang="ts">
  interface Props {
    fileListHidden?: boolean;
    onToggleFileList?: (() => void) | undefined;
  }

  const {
    fileListHidden = false,
    onToggleFileList,
  }: Props = $props();
</script>

<div data-testid="diff-toolbar">
  {#if fileListHidden && onToggleFileList}
    <button onclick={onToggleFileList}>Expand file tree</button>
  {/if}
</div>
