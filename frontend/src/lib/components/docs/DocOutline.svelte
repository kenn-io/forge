<script lang="ts">
  import type { HeadingEntry } from "./DocMarkdownView.svelte";

  interface Props {
    headings: HeadingEntry[];
    activeId: string | null;
    onSelect: (id: string) => void;
  }

  let { headings, activeId, onSelect }: Props = $props();

  // Normalize indentation so the leftmost level in the doc sits flush
  // against the panel edge. Docs that start at H2 shouldn't waste a full
  // indent level on a phantom H1.
  let baseLevel = $derived(headings.length === 0 ? 1 : Math.min(...headings.map((h) => h.level)));
</script>

<aside class="doc-outline" aria-label="Document outline">
  <h3 class="outline-heading">On this page</h3>
  {#if headings.length === 0}
    <p class="muted">No headings.</p>
  {:else}
    <ul class="outline-list">
      {#each headings as heading (heading.id)}
        <li>
          <button
            class="outline-link"
            class:active={activeId === heading.id}
            style:padding-left={`${(heading.level - baseLevel) * 12 + 8}px`}
            data-level={heading.level}
            onclick={() => onSelect(heading.id)}
          >
            {heading.text}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</aside>

<style>
  .doc-outline {
    padding: 14px 12px;
    overflow: auto;
    border-left: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .outline-heading {
    color: var(--text-muted);
    font-size: var(--font-size-3xs);
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    padding: 0 6px 8px;
  }

  .outline-list {
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }

  .outline-link {
    width: 100%;
    text-align: left;
    padding: 3px 8px;
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.35;
    transition: background 0.08s, color 0.08s, border-color 0.08s;
    border-left: 2px solid transparent;
  }

  .outline-link:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .outline-link.active {
    color: var(--accent-blue);
    border-left-color: var(--accent-blue);
    background: var(--accent-blue-soft);
  }

  .outline-link[data-level="1"] {
    font-weight: 600;
  }

  .outline-link[data-level="5"],
  .outline-link[data-level="6"] {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .muted {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    padding: 4px 6px;
  }
</style>
