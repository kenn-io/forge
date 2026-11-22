<script module lang="ts">
  export interface ItemTableColumns {
    /** Tracks for the columns before Repo, such as Activity's caret and Type. */
    leading?: string[];
    /** Grouping by repository drops the Repo column. */
    repo?: boolean;
    author?: boolean;
    when?: boolean;
    /** The title's minimum width. Repo and Author shrink to keep it, and only when the table is too narrow for them. */
    titleMin?: string | undefined;
  }

  /**
   * Column tracks for an item table: the grid that Activity and the pull request list share.
   * Rows are subgrids of the element that takes these tracks, so Repo and Author size to their
   * widest cell up to a cap, When to its content, and Item takes what is left.
   */
  export function itemTableColumns({
    leading = [],
    repo = true,
    author = true,
    when = true,
    titleMin = "0px",
  }: ItemTableColumns = {}): string {
    return [
      ...leading,
      repo ? "fit-content(var(--item-table-repo-max, 220px))" : "",
      author ? "fit-content(var(--item-table-author-max, 140px))" : "",
      `minmax(${titleMin}, 1fr)`,
      when ? "auto" : "",
      "24px",
    ].filter(Boolean).join(" ");
  }
</script>

<script lang="ts">
  import type { ClassValue } from "svelte/elements";

  interface Props {
    class?: ClassValue;
    /** Headers for the leading columns, one per leading track. */
    leading?: { label: string; class?: string }[];
    repo?: boolean;
    author?: boolean;
    when?: boolean;
  }

  const { class: className, leading = [], repo = true, author = true, when = true }: Props = $props();
</script>

<div class={["item-table-headers", className]}>
  {#each leading as column, index (index)}
    <span class={["cell", column.class]} aria-hidden={column.label === "" ? "true" : undefined}>{column.label}</span>
  {/each}
  {#if repo}
    <span class="cell cell--repo col-repo">Repo</span>
  {/if}
  {#if author}
    <span class="cell cell--author col-author">Author</span>
  {/if}
  <span class="cell cell--title">Item</span>
  {#if when}
    <span class="cell cell--time col-when">When</span>
  {/if}
  <span class="cell cell--link" aria-hidden="true"></span>
</div>

<style>
  /* A table sets the padding to match its rows, so the headers sit over the columns. */
  .item-table-headers {
    display: grid;
    grid-template-columns: subgrid;
    grid-column: 1 / -1;
    align-items: center;
    padding: var(--item-table-header-padding, 6px 0 4px);
    font-size: var(--font-size-2xs);
    font-weight: 500;
    text-transform: var(--label-transform, uppercase);
    letter-spacing: var(--letter-spacing-label, 0.5px);
    color: var(--text-muted);
    border-bottom: 1px solid var(--border-default);
    position: var(--item-table-header-position, sticky);
    top: 0;
    background: var(--item-table-header-bg, var(--bg-primary));
    z-index: 2;
  }

  .cell {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .cell--time {
    text-align: right;
  }
</style>
