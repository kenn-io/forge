<script lang="ts">
  import type { IssueSummary, KataAPI, IssueFilters, SearchScope } from "../../messages/types";
  import Modal from "./Modal.svelte";

  interface Props {
    open: boolean;
    kata: Pick<KataAPI, "search">;
    scope?: SearchScope | undefined;
    excludeIds?: ReadonlySet<number> | undefined;
    onClose: () => void;
    onPick: (issue: {
      id: number;
      uid: string;
      qualified_id: string;
      title: string;
    }) => void;
  }

  type PickableIssue = IssueSummary & { id: number };

  let {
    open,
    kata,
    scope = undefined,
    excludeIds = undefined,
    onClose,
    onPick,
  }: Props = $props();

  const SEARCH_DEBOUNCE_MS = 200;
  const MAX_RESULTS = 20;

  let query = $state("");
  let results = $state<PickableIssue[]>([]);
  let selected = $state<PickableIssue | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let searchGen = 0;
  let searchTimer: ReturnType<typeof setTimeout> | null = null;

  const visible = $derived(
    excludeIds === undefined
      ? results
      : results.filter((r) => !excludeIds.has(r.id)),
  );

  $effect(() => {
    if (!open) {
      if (searchTimer) clearTimeout(searchTimer);
      searchTimer = null;
      searchGen++;
      query = "";
      results = [];
      selected = null;
      loading = false;
      error = null;
    }
  });

  $effect(() => {
    if (!open) return;
    if (searchTimer) clearTimeout(searchTimer);
    const q = query.trim();
    searchGen++;
    selected = null;
    if (q === "") {
      results = [];
      loading = false;
      error = null;
      return;
    }
    const gen = searchGen;
    searchTimer = setTimeout(async () => {
      if (gen !== searchGen) return;
      loading = true;
      error = null;
      try {
        const filters: IssueFilters = {
          scope: scope ?? { kind: "all" },
          status: "open",
          owner: "",
          label: "",
          query: q,
        };
        const res = await kata.search(filters);
        if (gen !== searchGen) return;
        const found = res.issues.filter(hasIssueID);
        const filtered = excludeIds === undefined
          ? found
          : found.filter((issue) => !excludeIds.has(issue.id));
        results = filtered.slice(0, MAX_RESULTS);
      } catch (err) {
        if (gen !== searchGen) return;
        error = err instanceof Error ? err.message : "Search failed.";
        results = [];
      } finally {
        if (gen === searchGen) loading = false;
      }
    }, SEARCH_DEBOUNCE_MS);
  });

  function hasIssueID(issue: IssueSummary): issue is PickableIssue {
    return typeof issue.id === "number";
  }

  function handlePick(): void {
    if (!selected) return;
    onPick({
      id: selected.id,
      uid: selected.uid,
      qualified_id: selected.qualified_id,
      title: selected.title,
    });
  }
</script>

<Modal {open} title="Link to task" {onClose}>
  <div class="picker">
    <label class="picker-field">
      <span>Search tasks</span>
      <input
        type="search"
        bind:value={query}
        placeholder="Title or qualified ID..."
        autocomplete="off"
      />
    </label>
    {#if loading}
      <div class="picker-state">Searching...</div>
    {:else if visible.length === 0}
      <div class="picker-state">
        {query.trim() === "" ? "Type to search open tasks." : "No matches."}
      </div>
    {:else}
      <ul class="picker-results" role="listbox" aria-label="Matching tasks">
        {#each visible as r (r.uid)}
          <li role="option" aria-selected={selected?.uid === r.uid}>
            <button
              type="button"
              class="picker-result"
              class:active={selected?.uid === r.uid}
              onclick={() => (selected = r)}
            >
              <span class="picker-id">{r.qualified_id}</span>
              <span class="picker-title">{r.title}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
    {#if error}
      <div role="alert" class="picker-error">{error}</div>
    {/if}
  </div>
  {#snippet footer()}
    <button type="button" class="picker-action" onclick={onClose}>
      Cancel
    </button>
    <button
      type="button"
      class="picker-action primary"
      disabled={!selected}
      onclick={handlePick}
    >
      Link
    </button>
  {/snippet}
</Modal>

<style>
  .picker {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-width: min(360px, calc(100vw - 68px));
  }

  .picker-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .picker-field span {
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .picker-field input {
    width: 100%;
    padding: 6px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .picker-field input:focus {
    outline: 2px solid var(--accent-blue);
    outline-offset: -1px;
  }

  .picker-state {
    padding: 8px 10px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-style: italic;
  }

  .picker-results {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 280px;
    overflow-y: auto;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
  }

  .picker-result {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 8px;
    border: 0;
    background: transparent;
    color: var(--text-primary);
    text-align: left;
    font: inherit;
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .picker-result:hover {
    background: var(--bg-surface-hover);
  }

  .picker-result.active {
    background: var(--accent-blue-soft);
  }

  .picker-id {
    color: var(--accent-blue);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 600;
    white-space: nowrap;
  }

  .picker-title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .picker-error {
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: var(--accent-red-soft);
    color: var(--accent-red);
    font-size: var(--font-size-xs);
  }

  .picker-action {
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    cursor: pointer;
    font-size: var(--font-size-sm);
  }

  .picker-action:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .picker-action.primary {
    background: var(--accent-blue);
    border-color: var(--accent-blue);
    color: #ffffff;
  }

  .picker-action.primary:hover:not(:disabled) {
    filter: brightness(0.95);
  }

  .picker-action:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
</style>
