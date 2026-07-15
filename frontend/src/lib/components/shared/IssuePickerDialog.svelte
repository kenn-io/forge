<script lang="ts">
  import type { IssueSummary, KataAPI, IssueFilters, SearchScope } from "../../messages/types";
  import { Button, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
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
  const options = $derived<TypeaheadOption[]>(
    visible.map((issue) => ({
      name: issue.uid,
      label: issue.qualified_id,
      displayLabel: `${issue.qualified_id} ${issue.title}`,
      meta: issue.title,
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

  function updateQuery(nextQuery: string): void {
    if (nextQuery === "" && selected !== null) return;
    query = nextQuery;
  }

  function selectIssue(uid: string): void {
    selected = visible.find((issue) => issue.uid === uid) ?? null;
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
