<script lang="ts">
  import type { DiffFile } from "../../api/types.js";

  interface Props {
    file: DiffFile;
  }

  const { file }: Props = $props();
</script>

<div data-file-path={file.path}>
  {file.path}
  {#each file.hunks as hunk, hunkIndex (hunkIndex)}
    {#each hunk.lines as line, lineIndex (`${line.old_num ?? ""}:${line.new_num ?? ""}:${lineIndex}`)}
      {#if line.old_num != null || line.new_num != null}
        <button
          data-diff-path={file.path}
          data-diff-old-line={line.old_num}
          data-diff-new-line={line.new_num}
          type="button"
        >
          {line.content}
        </button>
      {/if}
    {/each}
  {/each}
</div>
