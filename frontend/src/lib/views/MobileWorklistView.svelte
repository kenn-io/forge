<script lang="ts">
  import { tick, untrack } from "svelte";
  import { Button, EmptyState, formatRelativeTime, ScrollBox, SelectDropdown, Spinner } from "@kenn-io/kit-ui";
  import ListChecksIcon from "@lucide/svelte/icons/list-checks";
  import type { WorklistItem } from "../api/worklists.js";
  import { getAppRuntime } from "../app/runtime-context.js";
  import MobileNavMenu from "../components/mobile/MobileNavMenu.svelte";
  import WorklistRow from "../components/worklists/WorklistRow.svelte";
  import { lastWorklistId, rememberWorklist } from "../components/worklists/worklist-memory.js";
  import {
    readWorklistOrder,
    readWorklistStateFilter,
    worklistItemDone,
    worklistItemKey,
    worklistItemsByScore,
    worklistItemSelection,
    worklistItemsInState,
    worklistRemainingLabel,
    worklistSections,
    writeWorklistStateFilter,
    type WorklistStateFilter,
  } from "../components/worklists/worklist-items.js";
  import { getNavigate, getStores } from "../context.js";
  import { buildMobileWorklistRoute, buildRoutedItemRoute } from "../routes.js";
  import {
    mobileListOriginState,
    rememberMobileListPosition,
    scrollViewportOf,
    takeMobileListPosition,
  } from "../stores/mobile-list-return.js";
  import { replaceUrl } from "../stores/router.svelte.js";

  interface Props {
    worklistId: number | null;
  }

  let { worklistId }: Props = $props();

  const runtime = getAppRuntime();
  const navigate = getNavigate();
  const { worklists } = getStores();

  const summaries = $derived(worklists.getSummaries());
  const loadedWorklist = $derived(worklists.getDetail());
  const detailError = $derived(worklists.getDetailError());

  // The phone view does not dismiss lists, so a list dismissed elsewhere is
  // simply missing here.
  const worklist = $derived(loadedWorklist?.dismissed_at === null ? loadedWorklist : null);
  const routedListMissing = $derived(
    worklistId === null ||
      (worklists.getOpenId() === worklistId &&
        (detailError?.kind === "notFound" || (loadedWorklist !== null && worklist === null))),
  );

  // Shared with the desktop rail, so both show the same rows.
  let stateFilter = $state<WorklistStateFilter>(readWorklistStateFilter());
  const order = readWorklistOrder();

  const shownItems = $derived(worklist ? worklistItemsInState(worklist.items, stateFilter) : []);
  const sections = $derived(
    order === "score" ? [{ section: "", items: worklistItemsByScore(shownItems) }] : worklistSections(shownItems),
  );
  const showSectionHeaders = $derived(order === "agent" && sections.some((section) => section.section !== ""));
  const doneCount = $derived(worklist ? worklist.items.filter(worklistItemDone).length : 0);
  const donePercent = $derived(worklist && worklist.items.length > 0 ? (doneCount / worklist.items.length) * 100 : 0);
  const showPicker = $derived(
    worklist !== null && summaries.length > 1 && summaries.some((summary) => summary.id === worklist.id),
  );

  const pickerOptions = $derived(
    summaries.map((summary) => ({
      value: String(summary.id),
      label: `${summary.title} · ${worklistRemainingLabel(summary.remaining_count)}`,
      triggerLabel: summary.title,
    })),
  );

  const stateOptions = [
    { value: "open", label: "Open" },
    { value: "closed", label: "Closed" },
    { value: "all", label: "All" },
  ];

  function setStateFilter(value: string): void {
    stateFilter = value === "closed" || value === "all" ? value : "open";
    writeWorklistStateFilter(stateFilter);
  }

  let root = $state<HTMLElement | null>(null);
  let pendingScrollTop = $state<number | null>(null);

  function positionKey(id: number): string {
    return `worklist:${id}`;
  }

  $effect(() => {
    const id = worklistId;
    pendingScrollTop = id === null ? null : (untrack(() => takeMobileListPosition(positionKey(id)))?.scrollTop ?? null);
  });

  $effect(() => {
    if (pendingScrollTop === null || shownItems.length === 0) return;
    const top = pendingScrollTop;
    pendingScrollTop = null;
    void tick().then(() => {
      const viewport = scrollViewportOf(root);
      if (viewport) viewport.scrollTop = top;
    });
  });

  $effect(() => {
    untrack(() =>
      runtime.runCommand(worklists.refreshSummariesEffect(), {
        operation: "refresh worklists on open",
        safeContext: {},
        onFailure: () => {},
      }),
    );
    return () =>
      untrack(() =>
        runtime.runCommand(worklists.openEffect(null), {
          operation: "close worklist",
          safeContext: {},
          onFailure: () => {},
        }),
      );
  });

  $effect(() => {
    const id = worklistId;
    untrack(() =>
      runtime.runCommand(worklists.openEffect(id), {
        operation: "open worklist",
        safeContext: { routed: id !== null },
        onFailure: () => {},
      }),
    );
  });

  // Without a usable routed list, reopen the last list shown here, else the newest.
  $effect(() => {
    if (!routedListMissing || !worklists.summariesLoaded()) return;
    const candidates = summaries.filter((summary) => summary.id !== worklistId);
    const remembered = untrack(lastWorklistId);
    const next = candidates.find((summary) => summary.id === remembered) ?? candidates[0];
    if (next) untrack(() => replaceUrl(buildMobileWorklistRoute(next.id)));
  });

  $effect(() => {
    const list = worklist;
    if (!list || list.id !== worklistId) return;
    untrack(() => rememberWorklist(list.id, null, liveListIds()));
  });

  function liveListIds(): number[] | null {
    return worklists.summariesLoaded() ? summaries.map((summary) => summary.id) : null;
  }

  function openItem(item: WorklistItem): void {
    const list = worklist;
    if (!list) return;
    rememberWorklist(list.id, worklistItemKey(item), liveListIds());
    // The whole list is loaded at once, so only the offset matters.
    rememberMobileListPosition(positionKey(list.id), {
      scrollTop: scrollViewportOf(root)?.scrollTop ?? 0,
      pageLimit: 0,
    });
    navigate(buildRoutedItemRoute(worklistItemSelection(item), { focus: true }), {
      state: mobileListOriginState("worklists"),
    });
  }

  function retry(): void {
    const effect = worklistId === null ? worklists.refreshSummariesEffect() : worklists.refreshAllEffect();
    runtime.runCommand(effect, { operation: "retry worklists", safeContext: {}, onFailure: () => {} });
  }

  const emptyFilterMessage = $derived(
    stateFilter === "closed" ? "No items are closed." : "No open items remain. Closed items are hidden.",
  );
</script>

<section class="mobile-worklist" aria-label="Worklists" bind:this={root}>
  <header class="mobile-worklist__bar">
    <h1 class="mobile-worklist__title">
      {#if showPicker && worklist}
        <SelectDropdown
          class="mobile-worklist__picker"
          value={String(worklist.id)}
          options={pickerOptions}
          title="Worklist"
          onchange={(value) => navigate(buildMobileWorklistRoute(Number(value)))}
        />
      {:else}
        {worklist?.title ?? "Worklists"}
      {/if}
    </h1>
    <MobileNavMenu />
  </header>

  {#if worklist}
    <div class="mobile-worklist__summary">
      <div class="mobile-worklist__progress">
        <span class="mobile-worklist__progress-bar" aria-hidden="true">
          <span style:width={`${donePercent}%`}></span>
        </span>
        <span title="Handled, merged, or closed">{doneCount} of {worklist.items.length} done</span>
      </div>
      <div class="mobile-worklist__meta">
        <span class="mobile-worklist__author">
          Written by {worklist.created_by.trim() || "an agent"} {formatRelativeTime(worklist.updated_at)}
        </span>
        <SelectDropdown
          class="mobile-worklist__state"
          value={stateFilter}
          options={stateOptions}
          title="Show"
          align="end"
          onchange={setStateFilter}
        />
      </div>
    </div>

    <ScrollBox label={`${worklist.title} items`} class="mobile-worklist__scroll">
      {#if worklist.items.length === 0}
        <p class="mobile-worklist__state-message">No items remain on this list.</p>
      {:else if shownItems.length === 0}
        <p class="mobile-worklist__state-message">{emptyFilterMessage}</p>
      {:else}
        {#each sections as section (section.section)}
          {#if showSectionHeaders}
            <h2 class="mobile-worklist__section">
              {section.section || "Other"}<span>{section.items.length}</span>
            </h2>
          {/if}
          <ul class="mobile-worklist__rows" aria-label={showSectionHeaders ? section.section || "Other" : worklist.title}>
            {#each section.items as item (worklistItemKey(item))}
              <li>
                <WorklistRow
                  {item}
                  opened={false}
                  multiSelected={false}
                  onclick={() => openItem(item)}
                  oncontextmenu={() => {}}
                />
              </li>
            {/each}
          </ul>
        {/each}
      {/if}
    </ScrollBox>
  {:else if detailError?.kind === "failed" || (routedListMissing && worklists.getSummariesError() !== null)}
    <div class="mobile-worklist__status" role="alert">
      <span>{detailError?.kind === "failed" ? detailError.message : worklists.getSummariesError()}</span>
      <Button size="sm" label="Retry" onclick={retry} />
    </div>
  {:else if routedListMissing && worklists.summariesLoaded() && summaries.length === 0}
    <div class="mobile-worklist__status">
      <EmptyState title="No worklists" description="Ask an agent connected to Forge to create one.">
        {#snippet icon()}<ListChecksIcon size={24} aria-hidden="true" />{/snippet}
      </EmptyState>
    </div>
  {:else}
    <div class="mobile-worklist__status" role="status"><Spinner size={16} />Loading worklist</div>
  {/if}
</section>

<style>
  .mobile-worklist {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding-bottom: env(safe-area-inset-bottom);
    background: var(--bg-primary);
  }

  .mobile-worklist__bar {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: var(--mobile-chrome-space-sm);
    min-height: calc(var(--mobile-chrome-hit-target) + 2 * var(--mobile-chrome-space-sm));
    padding: var(--mobile-chrome-space-sm) var(--mobile-chrome-space-md);
    border-bottom: thin solid var(--border-default);
    background: var(--bg-surface);
  }

  .mobile-worklist__title {
    flex: 1;
    min-width: 0;
    margin: 0;
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-weight: 650;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mobile-worklist__title :global(.mobile-worklist__picker) {
    width: 100%;
    min-width: 0;
  }

  .mobile-worklist__title :global(.mobile-worklist__picker .kit-select-dropdown__trigger) {
    width: 100%;
    min-height: var(--mobile-chrome-hit-target);
    font-size: var(--font-size-md);
    font-weight: 650;
  }

  .mobile-worklist__summary {
    flex-shrink: 0;
    display: flex;
    flex-direction: column;
    gap: var(--mobile-chrome-space-xs);
    padding: var(--mobile-chrome-space-sm) var(--mobile-chrome-space-md);
    border-bottom: thin solid var(--border-muted);
    background: var(--bg-surface);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .mobile-worklist__progress {
    display: flex;
    align-items: center;
    gap: var(--mobile-chrome-space-sm);
    white-space: nowrap;
  }

  .mobile-worklist__progress-bar {
    flex: 1;
    height: 4px;
    overflow: hidden;
    border-radius: 2px;
    background: var(--bg-inset);
  }

  .mobile-worklist__progress-bar span {
    display: block;
    height: 100%;
    background: var(--accent-green);
  }

  .mobile-worklist__meta {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--mobile-chrome-space-sm);
  }

  .mobile-worklist__author {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .mobile-worklist__meta :global(.mobile-worklist__state .kit-select-dropdown__trigger) {
    min-height: var(--mobile-chrome-hit-target);
    font-size: var(--font-size-sm);
  }

  .mobile-worklist :global(.mobile-worklist__scroll) {
    flex: 1;
    min-height: 0;
  }

  .mobile-worklist__section {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin: 0;
    padding: var(--mobile-chrome-space-xs) var(--mobile-chrome-space-md);
    border-bottom: thin solid var(--border-muted);
    background: var(--bg-inset);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    letter-spacing: var(--letter-spacing-label, 0.05em);
    text-transform: var(--label-transform, uppercase);
  }

  .mobile-worklist__section span {
    font-family: var(--font-mono);
    font-weight: 500;
  }

  .mobile-worklist__rows {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .mobile-worklist__rows :global(.worklist-row) {
    min-height: var(--mobile-chrome-hit-target);
    padding: var(--mobile-chrome-space-sm) var(--mobile-chrome-space-md);
  }

  .mobile-worklist__state-message {
    margin: 0;
    padding: var(--mobile-chrome-space-md);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .mobile-worklist__status {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--mobile-chrome-space-sm);
    padding: var(--mobile-chrome-space-md);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }
</style>
