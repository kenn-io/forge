<script lang="ts">
  import { untrack } from "svelte";
  import { SearchInput } from "@kenn-io/kit-ui";
  import { SelectDropdown } from "@middleman/ui";
  import type { MessagesCapabilities, MessagesSearchMode } from "../../api/messages/types";

  interface Props {
    capabilities: MessagesCapabilities;
    initialQuery: string;
    onSubmit: (query: string, mode: MessagesSearchMode) => void;
  }

  let { capabilities, initialQuery, onSubmit }: Props = $props();

  // `mode` is snapshot-on-mount: it's user-owned via the select after first
  // render, and capabilities don't change during a session. `untrack` reads
  // the first advertised mode once without triggering `state_referenced_locally`.
  let mode = $state<MessagesSearchMode>(untrack(() => capabilities.modes[0] ?? "fts"));

  // `query` mirrors the URL: the input shows whatever `initialQuery` is on
  // mount, AND tracks subsequent prop changes from back/forward navigation,
  // facet clicks, or external clears. Without the $effect sync, the input
  // would still show the old query after popstate or a facet-driven URL
  // update - the list would re-search but the input would lie about it.
  let query = $state(untrack(() => initialQuery));
  $effect(() => {
    // Re-seed on any prop change. We deliberately overwrite local edits
    // because the canonical state lives in the route - if the URL says
    // q="foo" and the user is typing, they have history to come back.
    query = initialQuery;
  });

  const multipleModesAvailable = $derived(capabilities.modes.length > 1);
  const modeOptions = $derived(
    capabilities.modes.map((item) => ({
      value: item,
      label: item,
    })),
  );

  function handleSubmit(event: SubmitEvent): void {
    event.preventDefault();
    // Always forward the trimmed value - even when it's empty. An empty
    // submit IS the user's "clear the query" gesture, and short-
    // circuiting here would prevent MessagesWorkspace from rewriting
    // route.q to null. The parent's handler decides whether the new
    // value is a no-op (same as current) and skips the route update in
    // that case; this component just reports what the user typed.
    onSubmit(query.trim(), mode);
  }
</script>

<form role="search" aria-label="Search messages" onsubmit={handleSubmit}>
  <div class="search-wrap">
    <SearchInput
      id="messages-search-input"
      name="q"
      bind:value={query}
      block
      placeholder="Search messages..."
      ariaLabel="Search messages"
    />
  </div>
  <SelectDropdown
    class="mode-dropdown"
    title="Search mode"
    value={mode}
    options={modeOptions}
    disabled={!multipleModesAvailable}
    onchange={(value) => {
      mode = value as MessagesSearchMode;
    }}
  />
  <button type="submit">Search</button>
</form>

<style>
  form {
    display: flex;
    gap: 6px;
    align-items: center;
    width: 100%;
  }

  .search-wrap {
    flex: 1;
    min-width: 0;
  }

  :global(.mode-dropdown) {
    min-width: 88px;
  }

  button[type="submit"] {
    padding: 5px 12px;
    border: 1px solid var(--border-default);
    border-radius: 4px;
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-md);
    cursor: pointer;
    white-space: nowrap;
  }

  button[type="submit"]:hover {
    background: var(--bg-surface-hover);
  }

</style>
