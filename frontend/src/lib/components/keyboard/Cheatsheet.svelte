<script lang="ts">
  import { tick, untrack } from "svelte";

  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import type { ModalFrameAction } from "@middleman/ui/stores/keyboard/keyspec";
  import { getStores, KbdBadge } from "@middleman/ui";
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

  let dialogEl: HTMLDivElement | undefined = $state();
  let filterEl: HTMLInputElement | undefined = $state();
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

  $effect(() => {
    if (!isCheatsheetOpen()) return;
    // Cheatsheet listens only for Escape — the Cmd+K / Cmd+P close bindings
    // are palette-only. Restricting the modal frame here keeps those chords
    // available for whoever owns them when this dialog isn't on top.
    const closeAction: ModalFrameAction = {
      id: "cheatsheet.close",
      label: "Close cheatsheet",
      binding: { key: "Escape" },
      priority: 100,
      when: () => true,
      handler: () => closeCheatsheet(),
    };
    const cleanup = untrack(() => pushModalFrame("cheatsheet", [closeAction]));
    void tick().then(() => filterEl?.focus());
    return cleanup;
  });

  // Focus trap: keep Tab / Shift+Tab cycling within the cheatsheet dialog so
  // focus never escapes to the page underneath while the dialog is open.
  // Initial focus is handled by the effect above via tick(); this trap only
  // intercepts subsequent Tab navigation. Mirrors Palette's pattern.
  $effect(() => {
    if (!isCheatsheetOpen() || !dialogEl) return;
    const focusable = (): HTMLElement[] =>
      Array.from(
        dialogEl!.querySelectorAll<HTMLElement>(
          "input, button, [tabindex]:not([tabindex='-1'])",
        ),
      ).filter((e) => !e.hasAttribute("disabled"));
    function trap(e: KeyboardEvent): void {
      if (e.key !== "Tab") return;
      const els = focusable();
      if (els.length === 0) return;
      const currentIndex = els.findIndex((el) => el === document.activeElement);
      const nextIndex = e.shiftKey
        ? (currentIndex <= 0 ? els.length - 1 : currentIndex - 1)
        : (currentIndex < 0 || currentIndex === els.length - 1 ? 0 : currentIndex + 1);
      els[nextIndex]?.focus();
      e.preventDefault();
    }
    const el = dialogEl;
    el.addEventListener("keydown", trap);
    return () => el.removeEventListener("keydown", trap);
  });
</script>

{#if isCheatsheetOpen()}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="cheatsheet-backdrop" onclick={closeCheatsheet}></div>
  <div
    bind:this={dialogEl}
    class="cheatsheet"
    role="dialog"
    aria-modal="true"
    aria-label="Keyboard shortcuts"
  >
    <input
      bind:this={filterEl}
      bind:value={filter}
      class="cheatsheet-filter"
      placeholder="Filter shortcuts…"
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
{/if}

<style>
  .cheatsheet-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    z-index: 100;
  }

  .cheatsheet {
    position: fixed;
    top: 80px;
    left: 50%;
    transform: translateX(-50%);
    width: 720px;
    max-width: calc(100vw - 32px);
    height: 540px;
    max-height: calc(100vh - 120px);
    display: grid;
    grid-template-rows: auto 1fr;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: 10px;
    box-shadow: var(--shadow-lg);
    z-index: 101;
  }

  .cheatsheet-filter {
    padding: 12px 16px;
    border: none;
    border-bottom: 1px solid var(--border-muted);
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    outline: none;
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
