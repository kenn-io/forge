<script module lang="ts">
  export interface ItemTableColumns {
    /** Tracks for the columns before Repo, such as Activity's caret and Type. */
    leading?: string[];
    /** Grouping by repository drops the Repo column. */
    repo?: boolean;
    author?: boolean;
    /** Pull request tables add a Status column between Item and When. */
    status?: boolean;
    when?: boolean;
    /** Each row is two lines: the item, then its other cells. The table takes the `item-table--stacked` class. */
    stacked?: boolean;
  }

  /**
   * The other columns take about 400px of a busy table. Narrower than this, too little is
   * left for titles, so the table gives each title its own line instead.
   */
  export const ITEM_TABLE_MIN_WIDTH = 720;

  /**
   * Column tracks for an item table: the grid that Activity and the pull request list share.
   * Rows are subgrids of the element that takes these tracks, so Repo and Author size to their
   * widest cell up to a cap, Status and When to their content, and Item takes what is left.
   * A stacked table has no Item track: the item spans the line above the other cells.
   * Item keeps a floor, so a busy row shortens Repo and Author before it costs the title.
   * The floor is capped at a share of the table, so a narrow pane or a large text size
   * never makes the table wider than the pane.
   * Stacked, Author keeps one, so a long repository name cannot push it off the line.
   */
  export function itemTableColumns({
    leading = [],
    repo = true,
    author = true,
    status = false,
    when = true,
    stacked = false,
  }: ItemTableColumns = {}): string {
    if (stacked) {
      // The author track is the line's flexible filler, so it stays when no author shows.
      return [
        ...leading,
        repo ? "[title-start repo-start] fit-content(var(--item-table-repo-max, 220px))" : "",
        `[${repo ? "" : "title-start "}author-start] minmax(4rem, 1fr)`,
        status ? "[status-start] auto" : "",
        when ? "[when-start] auto" : "",
        "[link-start] 24px",
      ].filter(Boolean).join(" ");
    }
    return [
      ...leading,
      repo ? "fit-content(var(--item-table-repo-max, 220px))" : "",
      author ? "fit-content(var(--item-table-author-max, 140px))" : "",
      "minmax(min(18rem, 45%), 1fr)",
      status ? "auto" : "",
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
    status?: boolean;
    when?: boolean;
  }

  const { class: className, leading = [], repo = true, author = true, status = false, when = true }: Props = $props();
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
  {#if status}
    <span class="cell cell--status">Status</span>
  {/if}
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

  .cell--status,
  .cell--time {
    text-align: right;
  }

  /* Stacked rows have no columns to name. */
  :global(.item-table--stacked) > .item-table-headers {
    display: none;
  }
</style>
