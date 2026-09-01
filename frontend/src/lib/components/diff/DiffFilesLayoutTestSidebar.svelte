<script lang="ts">
  interface Props {
    onCollapseFileTree?: (() => void) | undefined;
  }

  const { onCollapseFileTree }: Props = $props();
</script>

<aside data-testid="diff-sidebar">
  {#if onCollapseFileTree}
    <button onclick={onCollapseFileTree}>Collapse file tree</button>
  {/if}
</aside>
