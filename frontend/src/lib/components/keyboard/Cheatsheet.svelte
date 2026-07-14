<script lang="ts">
  import { getStores, KbdBadge } from "@middleman/ui";
  import { TextInput } from "@kenn-io/kit-ui";
  import Modal from "../shared/Modal.svelte";
  import {
    closeCheatsheet,
    isCheatsheetOpen,
  } from "../../stores/keyboard/cheatsheet-state.svelte.js";
  import { buildContext } from "../../stores/keyboard/context.svelte.js";
  import {
    getAllActions,
    getAllCheatsheetEntries,
  } from "../../stores/keyboard/registry.svelte.js";
  import { isActionVisible } from "../../stores/keyboard/visibility.js";
  import type {
    Action,
    CheatsheetEntry,
    ScopeTag,
  } from "../../stores/keyboard/types.js";

  // getStores() returns undefined when the cheatsheet is mounted outside the
  // <Provider> context (notably the unit-test fixture in
  // Cheatsheet.svelte.test.ts). In that case the visibility filter falls back
  // to surfacing every registered action so downstream tests can drive the
  // shell without setting up a full app context. Mirrors Palette.svelte.
  const stores = getStores() as ReturnType<typeof getStores> | undefined;

  let filter = $state("");

  const viewScope = $derived<ScopeTag | null>(
    stores
      ? (() => {
          const ctx = buildContext(stores);
          if (ctx.page === "pulls") return "view-pulls";
          if (ctx.page === "issues") return "view-issues";
          return null;
        })()
      : null,
  );

  // Visible actions honor the same when() gating the dispatcher does, except
  // in the no-Provider test fixture path where surfacing every action lets
  // unit tests drive grouping without standing up the full app context.
  const visibleActions = $derived<Action[]>(
    stores
      ? getAllActions().filter((a) => isActionVisible(a, buildContext(stores)))
      : getAllActions(),
  );

  const allCheatsheetEntries = $derived<CheatsheetEntry[]>(
    getAllCheatsheetEntries(),
  );

  function matchesFilter(label: string): boolean {
    if (filter === "") return true;
    return label.toLowerCase().includes(filter.toLowerCase());
  }

  // Group 1: actions whose scope matches the current view AND have a binding.
  // The "On this view" header should not surface palette-only commands; those
  // belong in the Commands section.
  const onThisViewActions = $derived<Action[]>(
    viewScope === null
      ? []
      : visibleActions
          .filter(
            (a) =>
              a.scope === viewScope &&
              a.binding !== null &&
              matchesFilter(a.label),
          )
          .slice()
          .sort((a, b) => a.label.localeCompare(b.label)),
  );

  // Group 2: global actions with a binding.
  const globalActions = $derived<Action[]>(
    visibleActions
      .filter(
        (a) =>
          a.scope === "global" &&
          a.binding !== null &&
          matchesFilter(a.label),
      )
      .slice()
      .sort((a, b) => a.label.localeCompare(b.label)),
  );

  // Group 3: every action with no binding (palette-only commands). These are
  // disjoint from the previous two groups by definition (binding-having vs
  // binding-less), so no dedup is needed across the cuts.
  const commandActions = $derived<Action[]>(
    visibleActions
      .filter((a) => a.binding === null && matchesFilter(a.label))
      .slice()
      .sort((a, b) => a.label.localeCompare(b.label)),
  );

  // Group 4: cheatsheet entries registered by component handlers (e.g.
  // RepoTypeahead arrow-nav). Section is hidden entirely when none exist so
  // we don't render an empty header.
  const componentEntries = $derived<CheatsheetEntry[]>(
    allCheatsheetEntries.filter((e) => matchesFilter(e.label)),
  );

  function bindingsOf(b: Action["binding"] | CheatsheetEntry["binding"]) {
    if (b === null) return [];
    return Array.isArray(b) ? b : [b];
  }
</script>

{#if isCheatsheetOpen()}
  <Modal
    open
    ariaLabel="Keyboard shortcuts"
    width={720}
    frameId="cheatsheet"
    onClose={closeCheatsheet}
  >
    <div class="cheatsheet">
      <TextInput
        class="cheatsheet-filter"
        block
        size="md"
        value={filter}
        placeholder="Filter shortcuts…"
        ariaLabel="Filter shortcuts"
        autofocus
        oninput={(value) => (filter = value)}
      />
      <div class="cheatsheet-body">
        {#if onThisViewActions.length > 0}
        <section class="cheatsheet-section">
          <div class="cheatsheet-section-header">On this view</div>
          {#each onThisViewActions as action (action.id)}
            <div class="cheatsheet-row">
              <span class="cheatsheet-row-label">{action.label}</span>
              <div class="cheatsheet-row-bindings">
                {#each bindingsOf(action.binding) as b, i (action.id + ":" + i)}
                  {#if i > 0}
                    <span class="cheatsheet-row-sep">or</span>
                  {/if}
                  <KbdBadge binding={b} />
                {/each}
              </div>
            </div>
          {/each}
        </section>
      {/if}
      {#if globalActions.length > 0}
        <section class="cheatsheet-section">
          <div class="cheatsheet-section-header">Global</div>
          {#each globalActions as action (action.id)}
            <div class="cheatsheet-row">
              <span class="cheatsheet-row-label">{action.label}</span>
              <div class="cheatsheet-row-bindings">
                {#each bindingsOf(action.binding) as b, i (action.id + ":" + i)}
                  {#if i > 0}
                    <span class="cheatsheet-row-sep">or</span>
                  {/if}
                  <KbdBadge binding={b} />
                {/each}
              </div>
            </div>
          {/each}
        </section>
      {/if}
      {#if commandActions.length > 0}
        <section class="cheatsheet-section">
          <div class="cheatsheet-section-header">Commands</div>
          {#each commandActions as action (action.id)}
            <div class="cheatsheet-row">
              <span class="cheatsheet-row-label">{action.label}</span>
              <div class="cheatsheet-row-bindings"></div>
            </div>
          {/each}
        </section>
      {/if}
      {#if componentEntries.length > 0}
        <section class="cheatsheet-section">
          <div class="cheatsheet-section-header">Component shortcuts</div>
          {#each componentEntries as entry (entry.id)}
            <div class="cheatsheet-row">
              <span class="cheatsheet-row-label">{entry.label}</span>
              <div class="cheatsheet-row-bindings">
                {#each bindingsOf(entry.binding) as b, i (entry.id + ":" + i)}
                  {#if i > 0}
                    <span class="cheatsheet-row-sep">or</span>
                  {/if}
                  <KbdBadge binding={b} />
                {/each}
              </div>
            </div>
          {/each}
        </section>
        {/if}
      </div>
    </div>
  </Modal>
{/if}

<style>
  :global(.kit-modal-body:has(> .modal-scope > .cheatsheet)) {
    padding: 0;
    overflow: hidden;
  }

  .cheatsheet {
    height: min(540px, calc(100vh - 120px));
    display: grid;
    grid-template-rows: auto minmax(0, 1fr);
  }

  :global(.cheatsheet-filter.kit-text-input) {
    height: 48px;
    padding: 0 16px;
    border: none;
    border-bottom: 1px solid var(--border-muted);
    border-radius: 0;
    background: transparent;
    font-size: var(--font-size-lg);
  }

  .cheatsheet-body {
    overflow-y: auto;
    padding: 8px 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .cheatsheet-section {
    padding: 4px 0;
  }

  .cheatsheet-section-header {
    padding: 6px 16px 4px;
    font-size: var(--font-size-2xs);
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--text-secondary);
  }

  .cheatsheet-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 6px 16px;
    color: var(--text-primary);
    font-size: var(--font-size-md);
  }

  .cheatsheet-row-label {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .cheatsheet-row-bindings {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  .cheatsheet-row-sep {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }
</style>
