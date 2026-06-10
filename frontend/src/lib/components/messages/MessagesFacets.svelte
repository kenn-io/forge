<script lang="ts">
  import type { MessagesAggregateRow } from "../../api/messages/types";

  interface FacetCategory {
    // null = loading; [] = empty; non-empty = render rows.
    rows: readonly MessagesAggregateRow[] | null;
  }

  interface Props {
    senders: FacetCategory;
    labels: FacetCategory;
    domains: FacetCategory;
    error: string | null;
    onSelectFacet: (token: string) => void;
    showLinkedView?: boolean | undefined;
    activeView?: "linked" | null | undefined;
    onSelectView?: ((view: "linked" | null) => void) | undefined;
  }

  let { senders, labels, domains, error, onSelectFacet, showLinkedView, activeView, onSelectView }: Props = $props();

  // Defensive cap: the parent already passes limit=20 to the API, but the
  // component re-applies the cap so a future caller can't accidentally
  // render hundreds of rows in the sidebar.
  const MAX_ROWS = 20;
</script>

<nav class="messages-facets" aria-label="Messages facets">
  {#if error}
    <div class="messages-facets-error" role="alert">{error}</div>
  {/if}

  {#if showLinkedView}
    <section class="facet-section facet-views">
      <h3>Views</h3>
      <ul class="facet-list">
        <li>
          <button type="button" class:active={activeView === null || activeView === undefined} aria-pressed={activeView === null || activeView === undefined} onclick={() => onSelectView?.(null)}>
            <span class="key">Search results</span>
          </button>
        </li>
        <li>
          <button type="button" class:active={activeView === "linked"} aria-pressed={activeView === "linked"} onclick={() => onSelectView?.("linked")}>
            <span class="key">Linked messages</span>
          </button>
        </li>
      </ul>
    </section>
  {/if}

  <section class="facet-section">
    <h3>Senders</h3>
    {#if senders.rows === null}
      <ul aria-busy="true" class="skel-list">
        {#each { length: 5 } as _, i (i)}
          <li class="skel"></li>
        {/each}
      </ul>
    {:else if senders.rows.length === 0}
      <p class="empty">No senders.</p>
    {:else}
      <ul class="facet-list">
        {#each senders.rows.slice(0, MAX_ROWS) as row (row.key)}
          <li>
            <button
              type="button"
              onclick={() => onSelectFacet(`from:${row.key}`)}
            >
              <span class="key">{row.key}</span>
              <span class="count">{row.count}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section class="facet-section">
    <h3>Labels</h3>
    {#if labels.rows === null}
      <ul aria-busy="true" class="skel-list">
        {#each { length: 5 } as _, i (i)}
          <li class="skel"></li>
        {/each}
      </ul>
    {:else if labels.rows.length === 0}
      <p class="empty">No labels.</p>
    {:else}
      <ul class="facet-list">
        {#each labels.rows.slice(0, MAX_ROWS) as row (row.key)}
          <li>
            <button
              type="button"
              onclick={() => onSelectFacet(`label:${row.key}`)}
            >
              <span class="key">{row.key}</span>
              <span class="count">{row.count}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section class="facet-section">
    <h3>Domains</h3>
    {#if domains.rows === null}
      <ul aria-busy="true" class="skel-list">
        {#each { length: 5 } as _, i (i)}
          <li class="skel"></li>
        {/each}
      </ul>
    {:else if domains.rows.length === 0}
      <p class="empty">No domains.</p>
    {:else}
      <ul class="facet-list">
        {#each domains.rows.slice(0, MAX_ROWS) as row (row.key)}
          <li>
            <button
              type="button"
              onclick={() => onSelectFacet(`domain:${row.key}`)}
            >
              <span class="key">{row.key}</span>
              <span class="count">{row.count}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</nav>

<style>
  .messages-facets {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 8px;
    height: 100%;
    overflow-y: auto;
    background: var(--bg-primary);
    font-size: var(--font-size-xs);
  }

  .messages-facets-error {
    margin: 0 4px 4px;
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: var(--accent-red-soft);
    color: var(--accent-red);
    font-size: var(--font-size-xs);
  }

  .facet-section h3 {
    margin: 0 0 4px 6px;
    font-size: var(--font-size-2xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-muted);
  }

  .facet-list,
  .skel-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .facet-list li,
  .skel-list li {
    margin: 0;
  }

  .facet-list button {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 4px 8px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-secondary);
    text-align: left;
    font: inherit;
    cursor: pointer;
    transition: background 0.07s;
  }

  .facet-list button:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .facet-list button:focus-visible {
    outline: none;
    background: var(--accent-blue-soft);
    color: var(--text-primary);
  }

  .facet-list button.active {
    background: var(--accent-blue-soft);
    color: var(--text-primary);
  }

  .key {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .count {
    flex-shrink: 0;
    color: var(--text-faint);
    font-variant-numeric: tabular-nums;
  }

  .empty {
    margin: 0 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-style: italic;
  }

  /* Skeleton placeholder bars during loading. */
  .skel {
    display: block;
    height: 10px;
    margin: 6px 8px;
    border-radius: 3px;
    background: var(--bg-inset);
    animation: shimmer 1.4s ease-in-out infinite;
  }

  @keyframes shimmer {
    0%   { opacity: 0.5; }
    50%  { opacity: 1; }
    100% { opacity: 0.5; }
  }
</style>
