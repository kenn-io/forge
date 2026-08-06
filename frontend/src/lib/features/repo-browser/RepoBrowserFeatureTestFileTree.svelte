<script lang="ts">
  import type { FileTreeEntry } from "../../components/diff/file-tree-entry.js";

  interface Props {
    entries: FileTreeEntry[];
    onSelect?: ((path: string) => void) | undefined;
  }

  let { entries, onSelect }: Props = $props();
</script>

{#each entries as entry (entry.path)}
  <button type="button" onclick={() => onSelect?.(entry.path)}>{entry.path}</button>
{/each}
