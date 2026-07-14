<script lang="ts">
  import { Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import type { DiffFile } from "../../api/types.js";
  import { getStores } from "../../context.js";

  interface Props {
    disabled?: boolean;
  }

  const { disabled = false }: Props = $props();
  const { diff } = getStores();

  const files = $derived(diff.getVisibleFileList()?.files ?? diff.getVisibleDiffFiles());
  const activeFile = $derived(diff.getActiveFile());
  const options = $derived<TypeaheadOption[]>(
    files.map((file) => ({
      name: file.path,
      label: fileName(file.path),
      displayLabel: file.path,
      meta: directory(file.path),
    })),
  );

  function fileName(path: string): string {
    const idx = path.lastIndexOf("/");
    return idx >= 0 ? path.slice(idx + 1) : path;
  }

  function directory(path: string): string {
    const idx = path.lastIndexOf("/");
    return idx >= 0 ? path.slice(0, idx) : "";
  }

  function selectFile(path: string): void {
    if (disabled) return;
    diff.requestScrollToFile(path);
  }
</script>

<div class="file-jump">
  <Typeahead
    {options}
    value={activeFile ?? ""}
    fallbackLabel="Jump to file"
    placeholder="Jump to file"
    title="Jump to file"
    disabled={disabled || files.length === 0}
    emptyLabel="No matching files"
    onselect={selectFile}
  />
</div>

<style>
  .file-jump {
    flex-shrink: 1;
    min-width: 0;
    --typeahead-min-width: 150px;
    --typeahead-max-width: min(320px, 36vw);
  }
</style>
