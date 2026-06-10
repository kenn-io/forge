<script lang="ts">
  import type { QuickView, SavedSearch } from "../../messages/savedSearches";

  interface Props {
    quickViews: readonly QuickView[];
    savedSearches: SavedSearch[];
    currentQuery: string;
    onApply: (query: string) => void;
    onSave: (name: string, query: string) => void;
    onDelete: (name: string) => void;
  }

  let {
    quickViews,
    savedSearches,
    currentQuery,
    onApply,
    onSave,
    onDelete,
  }: Props = $props();

  let savingOpen = $state(false);
  let draftName = $state("");
  let nameInput: HTMLInputElement | null = $state(null);

  // Disabled when there is no query to save (trim-aware so whitespace alone
  // does not unlock the affordance).
  const canSave = $derived(currentQuery.trim().length > 0);

  function startSave(): void {
    if (!canSave) return;
    // Prefill with the current query so a single Enter saves with the query
    // as the name; users can overwrite with a friendlier label.
    draftName = currentQuery;
    savingOpen = true;
    queueMicrotask(() => nameInput?.focus());
  }

  // Track whether keydown already handled the close, to avoid double-cancel
  // if the blur fires after Escape.
  let keyHandled = false;

  function cancelSave(): void {
    savingOpen = false;
    draftName = "";
    keyHandled = false;
  }

  function commitSave(): void {
    // Pass the raw draft - the helper trims and applies the empty-name
    // fallback. Avoids "   " (truthy whitespace) sneaking past `||`.
    onSave(draftName, currentQuery);
    savingOpen = false;
    draftName = "";
    keyHandled = false;
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      keyHandled = true;
      commitSave();
    } else if (event.key === "Escape") {
      event.preventDefault();
      keyHandled = true;
      cancelSave();
    }
  }

  function handleBlur(): void {
    if (keyHandled) return;
    cancelSave();
  }
</script>

<nav class="messages-saved-views" aria-label="Messages saved views">
  <section class="section">
    <h3>Quick views</h3>
    <ul class="list">
      {#each quickViews as view (view.label)}
        <li>
          <button
            type="button"
            class:active={view.query === currentQuery}
            aria-pressed={view.query === currentQuery}
            onclick={() => onApply(view.query)}
          >
            <span class="label">{view.label}</span>
          </button>
        </li>
      {/each}
    </ul>
  </section>

  <section class="section">
    <div class="section-head">
      <h3>Saved searches</h3>
      <button
        type="button"
        class="save-trigger"
        aria-label="Save current search"
        disabled={!canSave || savingOpen}
        onclick={startSave}
      >+ Save</button>
    </div>

    {#if savingOpen}
      <input
        bind:this={nameInput}
        bind:value={draftName}
        class="save-input"
        type="text"
        aria-label="Saved search name"
        onkeydown={handleKeydown}
        onblur={handleBlur}
      />
    {/if}

    {#if savedSearches.length === 0 && !savingOpen}
      <p class="empty">No saved searches yet.</p>
    {:else if savedSearches.length > 0}
      <ul class="list">
        {#each savedSearches as entry (entry.name)}
          <li class="row">
            <button
              type="button"
              class:active={entry.query === currentQuery}
              aria-pressed={entry.query === currentQuery}
              onclick={() => onApply(entry.query)}
            >
              <span class="label">{entry.name}</span>
            </button>
            <button
              type="button"
              class="delete"
              aria-label={`Delete saved search ${entry.name}`}
              onclick={() => onDelete(entry.name)}
            >x</button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</nav>

<style>
  .messages-saved-views {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px 8px 0;
    background: var(--bg-primary);
    font-size: var(--font-size-xs);
  }

  .section h3 {
    margin: 0 0 4px 6px;
    font-size: var(--font-size-2xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-muted);
  }

  .section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: 0 6px 4px;
  }

  .section-head h3 {
    margin: 0;
  }

  .save-trigger {
    padding: 2px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    cursor: pointer;
  }

  .save-trigger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .save-trigger:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .save-input {
    margin: 4px 6px;
    padding: 4px 8px;
    border: 1px solid var(--accent-blue);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-xs);
    box-shadow: 0 0 0 3px var(--accent-blue-soft);
    width: calc(100% - 12px);
    box-sizing: border-box;
  }

  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .list li {
    margin: 0;
  }

  .row {
    display: flex;
    align-items: center;
    gap: 4px;
    padding-right: 4px;
  }

  .row > button:first-child {
    flex: 1;
  }

  /* `:not(.delete)` keeps the per-row delete button from inheriting the
     full-width row styles below - `.list button` would otherwise outrank
     `.delete` (one element + one class) and collapse the apply button. */
  .list button:not(.delete) {
    display: flex;
    align-items: center;
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

  .list button:not(.delete):hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .list button:not(.delete).active {
    background: var(--accent-blue-soft);
    color: var(--text-primary);
  }

  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .delete {
    width: 20px;
    height: 20px;
    padding: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    line-height: 1;
  }

  .delete:hover {
    background: var(--accent-red-soft);
    color: var(--accent-red);
  }

  .empty {
    margin: 0 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-style: italic;
  }
</style>
