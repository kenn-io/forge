<script lang="ts">
  import ProviderIcon from "./provider/ProviderIcon.svelte";
  import TreeCheckbox from "./TreeCheckbox.svelte";
  import type { SelectionState } from "./repoTree.js";

  interface LabelSegment {
    text: string;
    match: boolean;
  }

  interface Props {
    kind: "host" | "owner" | "repo";
    label: string;
    ariaLabel: string;
    provider?: string | undefined;
    depth: number;
    hasChildren: boolean;
    expanded: boolean;
    selectionState: SelectionState;
    highlighted: boolean;
    segments?: LabelSegment[] | undefined;
    onToggleExpand: () => void;
    onToggleSelect: () => void;
    onHover?: () => void;
  }

  let {
    kind,
    label,
    ariaLabel,
    provider,
    depth,
    hasChildren,
    expanded,
    selectionState,
    highlighted,
    segments,
    onToggleExpand,
    onToggleSelect,
    onHover,
  }: Props = $props();

  function rowMouseDown() {
    // Name/body click expands interior rows, selects leaves.
    if (hasChildren) onToggleExpand();
    else onToggleSelect();
  }

  function checkboxMouseDown(event: MouseEvent) {
    // stopPropagation keeps the row from also toggling expand; preventDefault
    // keeps focus on the filter input (the checkbox is a real focusable input,
    // and stopping propagation skips the list's preventBlur handler). Without
    // it a real click steals focus and breaks keyboard navigation.
    event.stopPropagation();
    event.preventDefault();
    onToggleSelect();
  }

  function caretMouseDown(event: MouseEvent) {
    // Same reasoning as the checkbox: keep focus on the filter input.
    event.stopPropagation();
    event.preventDefault();
  }

  function caretClick(event: MouseEvent) {
    event.stopPropagation();
    onToggleExpand();
  }
</script>

<li
  class="typeahead-option repo-tree-row"
  class:highlighted
  role="option"
  aria-label={ariaLabel}
  aria-selected={selectionState === "checked"}
  aria-checked={selectionState === "partial"
    ? "mixed"
    : selectionState === "checked"}
  style:padding-left={`${6 + depth * 14}px`}
  onmousedown={rowMouseDown}
  onmouseenter={() => onHover?.()}
>
  {#if hasChildren}
    <button
      class="repo-tree-caret"
      class:expanded
      aria-label={`Toggle ${label}`}
      aria-expanded={expanded}
      onclick={caretClick}
      onmousedown={caretMouseDown}
      type="button"
    >
      <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true">
        <polyline
          points="2,3 5,7 8,3"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
        />
      </svg>
    </button>
  {:else}
    <span class="repo-tree-caret repo-tree-caret--leaf" aria-hidden="true"></span>
  {/if}

  <TreeCheckbox value={selectionState} onmousedown={checkboxMouseDown} />

  {#if kind === "host" && provider}
    <ProviderIcon {provider} size={14} />
  {/if}

  <span class="repo-tree-label">
    {#if segments}{#each segments as seg, segIndex (`${ariaLabel}-${segIndex}-${seg.text}-${seg.match}`)}{#if seg.match}<mark class="match">{seg.text}</mark>{:else}{seg.text}{/if}{/each}{:else}{label}{/if}
  </span>
</li>

<style>
  .repo-tree-row {
    gap: 6px;
  }

  .repo-tree-caret {
    width: 12px;
    height: 12px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    padding: 0;
    border: none;
    background: none;
    color: var(--text-muted);
    cursor: pointer;
  }

  .repo-tree-caret svg {
    transform: rotate(-90deg);
    transition: transform 0.12s ease;
  }

  .repo-tree-caret.expanded svg {
    transform: rotate(0deg);
  }

  .repo-tree-caret--leaf {
    cursor: default;
  }

  .repo-tree-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
