<script lang="ts">
  import { Button, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import type { KataTaskReference, KataTaskReferenceSearch } from "../../api/kata/snapshot.js";
  import Modal from "./Modal.svelte";

  interface Props {
    open: boolean;
    searchReferences: KataTaskReferenceSearch;
    daemonId?: string | undefined;
    excludeUIDs?: ReadonlySet<string> | undefined;
    onClose: () => void;
    onPick: (issue: KataTaskReference) => void;
  }

  let {
    open,
    searchReferences,
    daemonId = undefined,
    excludeUIDs = undefined,
    onClose,
    onPick,
  }: Props = $props();

  const SEARCH_DEBOUNCE_MS = 200;
  const MAX_RESULTS = 20;
  const REFERENCE_FETCH_LIMIT = 50;

  let query = $state("");
  let results = $state.raw<KataTaskReference[]>([]);
  let selected = $state.raw<KataTaskReference | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let searchGen = 0;
  let searchTimer: ReturnType<typeof setTimeout> | null = null;

  const visible = $derived(
    excludeUIDs === undefined
      ? results
      : results.filter((result) => !excludeUIDs.has(result.uid)),
  );
  const options = $derived<TypeaheadOption[]>(
    visible.map((reference) => ({
      name: reference.uid,
      label: reference.reference,
      displayLabel: `${reference.reference} ${reference.title}`,
      meta: reference.title,
    })),
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
        const res = await searchReferences(q, {
          ...(daemonId ? { daemon_id: daemonId } : {}),
          limit: REFERENCE_FETCH_LIMIT,
        });
        if (gen !== searchGen) return;
        const found = res.references ?? [];
        const filtered = excludeUIDs === undefined
          ? found
          : found.filter((reference) => !excludeUIDs.has(reference.uid));
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

  function updateQuery(nextQuery: string): void {
    if (nextQuery === "" && selected !== null) return;
    query = nextQuery;
  }

  function selectIssue(uid: string): void {
    selected = visible.find((issue) => issue.uid === uid) ?? null;
  }

  function handlePick(): void {
    if (!selected) return;
    onPick(selected);
  }
</script>

<Modal {open} title="Link to task" {onClose}>
  <div class="picker">
    <div class="picker-field">
      <span>Search tasks</span>
      <Typeahead
        remote
        {options}
        value={selected?.uid ?? ""}
        fallbackLabel="Select a task"
        placeholder="Title or qualified ID..."
        emptyLabel={query.trim() === "" ? "Type to search open tasks." : "No matches."}
        loading={loading}
        loadingLabel="Searching..."
        error={error ?? ""}
        onquery={updateQuery}
        onselect={selectIssue}
      />
    </div>
  </div>
  {#snippet footer()}
    <Button size="sm" onclick={onClose}>Cancel</Button>
    <Button size="sm" surface="solid" disabled={!selected} onclick={handlePick}>Link</Button>
  {/snippet}
</Modal>

<style>
  .picker {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
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

  .picker-field :global(.kit-typeahead) {
    width: 100%;
    max-width: none;
    --typeahead-control-height: 32px;
    --typeahead-control-font-size: var(--font-size-sm);
  }
</style>
