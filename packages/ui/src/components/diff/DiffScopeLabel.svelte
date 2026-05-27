<script lang="ts">
  import type { DiffScope } from "../../stores/diff.svelte.js";
  import { formatDiffScopeLabel } from "./scope-label.js";

  interface Props {
    scope: DiffScope;
  }

  const { scope }: Props = $props();

  const label = $derived(formatDiffScopeLabel(scope));
  const dirty = $derived(scope.kind !== "head");
</script>

<span class="diff-scope-label" class:diff-scope-label--dirty={dirty}>
  {label}
</span>

<style>
  .diff-scope-label {
    display: inline-flex;
    align-items: center;
    min-width: 0;
    overflow: hidden;
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    line-height: 1;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .diff-scope-label--dirty {
    color: var(--accent-amber);
  }
</style>
