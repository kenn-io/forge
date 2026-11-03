<script lang="ts">
  import {
    Button,
    Chip,
    EmptyState,
    FilterDropdown,
    SearchInput,
    Spinner,
    formatRelativeTime,
    type FilterDropdownSection,
    type MenuAnchorPoint,
  } from "@kenn-io/kit-ui";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import ListOrderedIcon from "@lucide/svelte/icons/list-ordered";
  import { Effect } from "effect";
  import type { Snippet } from "svelte";
  import { onMount, untrack } from "svelte";
  import type { ApiProblemError, TransientTransportError } from "../../api/effect-errors.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import { retryIdempotentRead } from "../../api/retry-policy.js";
  import { apiErrorMessage } from "../../api/runtime.js";
  import type { PullRequest } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppServices } from "../../app/runtime.js";
  import { getStores } from "../../context.js";
  import { pollWhileVisible } from "../../effect/poll-while-visible.js";
  import { makeLatestSharedRead } from "../../effect/latest-shared-read.js";
  import { buildMergeQueuePullRoute, type PullRequestRouteRef } from "../../routes.js";
  import { getGlobalRepo } from "../../stores/filter.svelte.js";
  import { isPullMerging } from "../../stores/pull-merges.svelte.js";
  import type { ListItemTarget } from "../../stores/item-actions.js";
  import { navigate, replaceUrl, withBasePath } from "../../stores/router.svelte.js";
  import {
    createRangeSelection,
    isSelectAllShortcut,
    preventShiftTextSelection,
  } from "../../utils/range-selection.svelte.js";
  import { localDateTimeLabel } from "../../utils/time.js";
  import PRListView from "../../views/PRListView.svelte";
  import type { InlineWorkspaceController } from "../../workspace-inline.js";
  import type { TabbedPanelLeaf } from "../shared/tabbed-panel-layout.js";
  import { repoIdentityMatches } from "../detail/detail-match.js";
  import {
    formatMergeQueueEta,
    hasMergeQueuePosition,
    mergeQueueEntryDescription,
    mergeQueueRemovalDescription,
    mergeQueueRemovalReason,
    mergeQueueState,
  } from "../detail/merge-queue.js";
  import RepoPrivateIcon from "../shared/RepoPrivateIcon.svelte";
  import ItemActionsMenu from "../sidebar/ItemActionsMenu.svelte";
  import WorkspaceIndicator from "../shared/WorkspaceIndicator.svelte";
  import {
    emptyMergeQueueFilters,
    filterMergeQueuePulls,
    groupMergeQueues,
    loadMergeQueueFilters,
    mergeQueueFiltersActive,
    mergeQueueStatusKey,
    saveMergeQueueFilters,
    type MergeQueueFilters,
  } from "./merge-queues.js";

  type DetailTab = "conversation" | "files";
  type ReadError = ApiProblemError | TransientTransportError;

  interface Props {
    selectedPR?: PullRequestRouteRef | null;
    detailTab?: DetailTab;
    isSidebarCollapsed?: boolean;
    sidebarWidth?: number;
    sidebarOverlay?: boolean;
    onSidebarResize?: (width: number) => void;
    inlineWorkspace?: InlineWorkspaceController | null;
    workspacePaneControls?: Snippet<[TabbedPanelLeaf]> | undefined;
  }

  const {
    selectedPR = null,
    detailTab = "conversation",
    isSidebarCollapsed = false,
    sidebarWidth = 340,
    sidebarOverlay = false,
    onSidebarResize,
    inlineWorkspace = null,
    workspacePaneControls = undefined,
  }: Props = $props();

  const runtime = getAppRuntime();
  const { events, detail } = getStores();

  let pulls = $state.raw<PullRequest[]>([]);
  let loaded = $state(false);
  let loadError = $state<string | null>(null);
  let filters = $state.raw<MergeQueueFilters>(loadMergeQueueFilters());
  let refresh: (() => void) | null = null;

  const visiblePulls = $derived(filterMergeQueuePulls(pulls, filters));
  const groups = $derived(groupMergeQueues(visiblePulls));
  const filtering = $derived(mergeQueueFiltersActive(filters));
  const queuedCount = $derived(visiblePulls.filter((pr) => pr.merge_queue).length);
  const removedCount = $derived(visiblePulls.length - queuedCount);
  const summary = $derived.by(() => {
    const parts = [`${queuedCount} queued`];
    if (removedCount > 0) parts.push(`${removedCount} removed`);
    if (filtering) parts.push(`${visiblePulls.length} of ${pulls.length} shown`);
    return parts.join(" · ");
  });

  const statusOrder = ["mergeable", "awaiting_checks", "queued", "locked", "unmergeable"];

  function statusLabel(key: string): string {
    return key === "removed" ? "Removed" : mergeQueueState(key).label;
  }

  function statusRank(key: string): number {
    if (key === "removed") return statusOrder.length + 1;
    const index = statusOrder.indexOf(key);
    return index === -1 ? statusOrder.length : index;
  }

  function counts(
    keyOf: (pr: PullRequest) => string,
    labelOf: (pr: PullRequest, key: string) => string,
  ): Map<string, { label: string; count: number }> {
    const result = new Map<string, { label: string; count: number }>();
    for (const pr of pulls) {
      const key = keyOf(pr);
      const current = result.get(key);
      if (current) current.count++;
      else result.set(key, { label: labelOf(pr, key), count: 1 });
    }
    return result;
  }

  function toggled(list: readonly string[], value: string): string[] {
    return list.includes(value) ? list.filter((item) => item !== value) : [...list, value];
  }

  function setFilters(next: MergeQueueFilters): void {
    filters = next;
    saveMergeQueueFilters(next);
  }

  // Options come from the loaded pull requests, so a chosen value that is no
  // longer present still shows, with no count, and can be cleared.
  function optionItems(
    available: Map<string, { label: string; count: number }>,
    chosen: readonly string[],
    field: "statuses" | "authors",
    order: (a: [string, { label: string }], b: [string, { label: string }]) => number,
  ) {
    const entries = [...available.entries()];
    for (const value of chosen) {
      if (!available.has(value)) entries.push([value, { label: field === "statuses" ? statusLabel(value) : value, count: 0 }]);
    }
    return entries.sort(order).map(([value, { label, count }]) => ({
      id: `${field}:${value}`,
      label,
      ...(count > 0 && { count }),
      active: chosen.includes(value),
      onSelect: () => setFilters({ ...filters, [field]: toggled(filters[field], value) }),
    }));
  }

  const statusSections = $derived<FilterDropdownSection[]>([
    {
      title: "Status",
      items: optionItems(counts(mergeQueueStatusKey, (_, key) => statusLabel(key)), filters.statuses, "statuses", ([a], [b]) => statusRank(a) - statusRank(b)),
    },
  ]);
  const authorSections = $derived<FilterDropdownSection[]>([
    {
      title: "Author",
      items: optionItems(
        counts((pr) => pr.Author, authorLabel),
        filters.authors,
        "authors",
        ([, a], [, b]) => a.label.localeCompare(b.label),
      ),
    },
  ]);

  function readErrorMessage(failure: ReadError): string {
    return failure._tag === "ApiProblemError"
      ? apiErrorMessage(failure.problem, "Could not load merge queues.")
      : "Could not reach Forge to load merge queues.";
  }

  // Only open pull requests have queue state, and the server keeps the ones
  // in a queue or last removed from one.
  function listQueuedPulls(repo: string | undefined) {
    return executeGeneratedApiRequest("GET /pulls merge queues", (client, signal) =>
      client.PullRequestsService.listPulls(
        { state: "open", attributes: "merge_queue", ...(repo !== undefined && { repo }) },
        { signal },
      ),
    ).pipe(retryIdempotentRead, Effect.map((result) => result ?? []));
  }

  function routeRef(pr: PullRequest): PullRequestRouteRef {
    return {
      provider: pr.repo.provider,
      platformHost: pr.repo.platform_host,
      repositoryKey: repositoryKeyFromWire(pr.repo),
      owner: pr.repo.owner,
      name: pr.repo.name,
      repoPath: pr.repo.repo_path,
      number: pr.Number,
    };
  }

  function isSelected(pr: PullRequest): boolean {
    return selectedPR !== null && pr.Number === selectedPR.number && repoIdentityMatches(pr, selectedPR);
  }

  // Every row in on-screen order: queued entries, then removals, per queue.
  const rows = $derived(groups.flatMap((group) => [...group.entries, ...group.removals]));
  const selection = createRangeSelection<PullRequest>({
    rows: () => rows,
    key: (pr) => String(pr.ID),
    openedKey: () => {
      const opened = rows.find(isSelected);
      return opened ? String(opened.ID) : null;
    },
  });
  let contextMenu = $state.raw<{
    targets: ListItemTarget[];
    anchor: MenuAnchorPoint;
    row: HTMLElement | undefined;
  } | null>(null);
  let contextMenuOpen = $state(false);

  // Plain clicks open the pull request in-app and keep the current detail
  // tab; shift and Cmd/Ctrl clicks select rows like the PR list. Middle and
  // Alt clicks keep the href.
  function handleRowClick(event: MouseEvent, pr: PullRequest): void {
    if (event.button !== 0 || event.altKey) return;
    event.preventDefault();
    if (!selection.activate(event, pr)) return;
    navigate(buildMergeQueuePullRoute(routeRef(pr), detailTab === "files" ? "files" : undefined));
  }

  function openContextMenu(event: MouseEvent, pr: PullRequest): void {
    event.preventDefault();
    // Right-clicking outside the selection acts on that PR alone.
    const multi = selection.selected.length > 1 && selection.has(pr);
    if (!multi) selection.clear();
    contextMenu = {
      targets: (multi ? selection.selected : [pr]).map((item) => ({ kind: "pull", item })),
      anchor: { x: event.clientX, y: event.clientY },
      row: event.currentTarget instanceof HTMLElement ? event.currentTarget : undefined,
    };
    contextMenuOpen = true;
  }

  // Menu actions change items on the provider; the open PR reloads too, in
  // case it was one of them.
  function reloadAfterItemAction(): void {
    refresh?.();
    if (selectedPR !== null) {
      detail.refreshDetailOnly(selectedPR.owner, selectedPR.name, selectedPR.number, {
        provider: selectedPR.provider,
        platformHost: selectedPR.platformHost,
        repoPath: selectedPR.repoPath,
      });
    }
  }

  function handleSelectionKeys(event: KeyboardEvent): void {
    if (event.key === "Escape" && selection.active) {
      event.preventDefault();
      selection.clear();
    } else if (isSelectAllShortcut(event)) {
      event.preventDefault();
      selection.selectAll();
    }
  }

  function changeDetailTab(tab: DetailTab, options?: { replace?: boolean }): void {
    if (selectedPR === null) return;
    const path = buildMergeQueuePullRoute(selectedPR, tab === "files" ? "files" : undefined);
    if (options?.replace) replaceUrl(path);
    else navigate(path);
  }

  function openStackMember(ref: PullRequestRouteRef): boolean {
    navigate(buildMergeQueuePullRoute(ref));
    return true;
  }

  function merging(pr: PullRequest): boolean {
    return isPullMerging(
      { provider: pr.repo.provider, platformHost: pr.repo.platform_host, repositoryKey: repositoryKeyFromWire(pr.repo) },
      pr.Number,
    );
  }

  function authorLabel(pr: PullRequest): string {
    return pr.AuthorDisplayName || pr.Author;
  }

  onMount(() => {
    const lifecycle = runtime.runCommand(
      Effect.scoped(
        Effect.gen(function* () {
          const reads = yield* makeLatestSharedRead<PullRequest[], ReadError, AppServices>();
          const readOnce = Effect.suspend(() => {
            const repo = getGlobalRepo();
            return reads.read(JSON.stringify(repo ?? null), listQueuedPulls(repo));
          }).pipe(
            Effect.match({
              onSuccess: (result) => {
                pulls = result;
                loadError = null;
                loaded = true;
              },
              onFailure: (failure) => {
                loadError = readErrorMessage(failure);
              },
            }),
          );
          yield* Effect.acquireRelease(
            Effect.sync(() => {
              refresh = () => {
                runtime.runCommand(readOnce, {
                  operation: "merge-queues.read",
                  safeContext: { surface: "merge-queues" },
                  onFailure: () => undefined,
                });
              };
              return events.subscribeWorkspaceEvents((event) => {
                if (event.type === "data_changed" || event.type === "open") refresh?.();
              });
            }),
            (unsubscribe) =>
              Effect.sync(() => {
                unsubscribe();
                refresh = null;
              }),
          );
          // The poll also keeps the relative times current. It launches reads
          // rather than awaiting them: a read for another repository filter
          // interrupts the one in flight, and that must not end this loop.
          yield* pollWhileVisible(Effect.sync(() => refresh?.()), "30 seconds", { immediate: true });
        }),
      ),
      {
        operation: "merge-queues.lifecycle",
        safeContext: { surface: "merge-queues" },
        onFailure: () => undefined,
      },
    );
    return () => lifecycle.interrupt();
  });

  // The header repository filter narrows the page like the PR list.
  $effect(() => {
    getGlobalRepo();
    untrack(() => refresh?.());
  });
</script>

<PRListView
  {selectedPR}
  {detailTab}
  {isSidebarCollapsed}
  {sidebarWidth}
  {sidebarOverlay}
  {...onSidebarResize ? { onSidebarResize } : {}}
  {inlineWorkspace}
  {workspacePaneControls}
  onDetailTabChange={changeDetailTab}
  onStackMemberNavigate={openStackMember}
  placeholderHint="Select a pull request from a merge queue"
  sidebarList={queueList}
/>

{#snippet queueList()}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="merge-queues" onkeydown={handleSelectionKeys}>
    <header class="merge-queues__header">
      <div class="merge-queues__heading">
        <h1 class="merge-queues__title">Merge queues</h1>
        {#if loaded && pulls.length > 0}
          <span class="merge-queues__summary">{summary}</span>
        {/if}
      </div>
      <div class="merge-queues__toolbar">
        <SearchInput
          value={filters.query}
          size="sm"
          block
          placeholder="Search queues"
          ariaLabel="Search merge queues"
          oninput={(query) => setFilters({ ...filters, query })}
          onclear={() => setFilters({ ...filters, query: "" })}
        />
        <FilterDropdown
          label="Status"
          title="Filter by status"
          active={filters.statuses.length > 0}
          badgeCount={filters.statuses.length}
          sections={statusSections}
          resetLabel="Any status"
          onReset={() => setFilters({ ...filters, statuses: [] })}
          emptyLabel="No statuses"
          minWidth="190px"
          align="end"
        />
        <FilterDropdown
          label="Author"
          title="Filter by author"
          active={filters.authors.length > 0}
          badgeCount={filters.authors.length}
          sections={authorSections}
          resetLabel="Any author"
          onReset={() => setFilters({ ...filters, authors: [] })}
          searchable
          searchPlaceholder="Find an author"
          emptyLabel="No authors"
          minWidth="220px"
          align="end"
        />
      </div>
    </header>

    <div class="merge-queues__body">
      {#if loaded && loadError !== null}
        <p class="merge-queues__error" role="alert">{loadError}</p>
      {/if}
      {#if !loaded && loadError === null}
        <div class="merge-queues__state" role="status"><Spinner size={16} />Loading merge queues</div>
      {:else if !loaded && loadError !== null}
        <EmptyState title="Could not load merge queues" description={loadError}>
          <Button size="sm" onclick={() => refresh?.()}>Retry</Button>
        </EmptyState>
      {:else if pulls.length === 0}
        <EmptyState
          title="No pull requests are in a merge queue"
          description="Pull requests you add to a GitHub merge queue appear here, with their position and state, until they merge."
        >
          {#snippet icon()}<ListOrderedIcon size={22} strokeWidth={1.75} />{/snippet}
        </EmptyState>
      {:else if groups.length === 0}
        <EmptyState title="No pull requests match" description="Change the search or filters to see more of the queues.">
          <Button size="sm" onclick={() => setFilters(emptyMergeQueueFilters)}>Clear search and filters</Button>
        </EmptyState>
      {:else}
        {#each groups as group (group.key)}
          <section class="merge-queue" aria-label={`${group.repo.repo_path} ${group.branch} merge queue`}>
            <header class="merge-queue__header">
              <h2 class="merge-queue__title">
                <span class="merge-queue__repo">{group.repo.repo_path}</span>
                <RepoPrivateIcon isPrivate={group.repo.private} />
                <code class="merge-queue__branch">{group.branch}</code>
              </h2>
              <span class="merge-queue__counts">
                {group.entries.length}{#if group.removals.length > 0}&nbsp;· {group.removals.length} out{/if}
              </span>
              {#if group.providerURL}
                <a
                  class="merge-queue__provider-link"
                  href={group.providerURL}
                  target="_blank"
                  rel="noopener noreferrer"
                  title="Open this merge queue on GitHub"
                  aria-label="Open this merge queue on GitHub"
                >
                  <ExternalLinkIcon size={12} strokeWidth={2} aria-hidden="true" />
                </a>
              {/if}
            </header>

            {#if group.entries.length > 0}
              <ol class="merge-queue__rows" aria-label="Queued pull requests">
                {#each group.entries as pr (pr.ID)}
                  {@const entry = pr.merge_queue!}
                  {@const state = mergeQueueState(entry.state)}
                  <li>
                    <a
                      class="merge-queue__row"
                      class:merge-queue__row--selected={isSelected(pr)}
                      class:multi-selected={selection.has(pr)}
                      aria-current={isSelected(pr) ? "true" : undefined}
                      href={withBasePath(buildMergeQueuePullRoute(routeRef(pr)))}
                      title={mergeQueueEntryDescription(entry)}
                      onmousedown={preventShiftTextSelection}
                      onclick={(event) => handleRowClick(event, pr)}
                      oncontextmenu={(event) => openContextMenu(event, pr)}
                    >
                      <span
                        class="merge-queue__position"
                        title={hasMergeQueuePosition(entry) ? undefined : "The queue has not reported a position yet"}
                      >
                        {hasMergeQueuePosition(entry) ? entry.position : "–"}
                      </span>
                      <span class="merge-queue__item">
                        <span class="merge-queue__line">
                          <span class="merge-queue__pr-title">{pr.Title}</span>
                          <span class="merge-queue__pr-number">#{pr.Number}</span>
                          {#if pr.workspace}<WorkspaceIndicator status={pr.workspace.status} size={12} />{/if}
                          {#if merging(pr)}
                            <span class="merge-queue__merging"><Spinner size={11} />Merging</span>
                          {/if}
                          {#if selection.has(pr)}<span class="kit-sr-only">, selected</span>{/if}
                        </span>
                        <span class="merge-queue__line">
                          <Chip size="sm" tone={state.tone}>{state.label}</Chip>
                          <span class="merge-queue__meta">
                            {authorLabel(pr)} ·
                            <time datetime={entry.enqueued_at} title={localDateTimeLabel(entry.enqueued_at)}
                              >{formatRelativeTime(entry.enqueued_at)}</time
                            >
                            {#if entry.estimated_time_to_merge_seconds !== undefined && entry.estimated_time_to_merge_seconds > 0}
                              · merges in {formatMergeQueueEta(entry.estimated_time_to_merge_seconds)}
                            {/if}
                          </span>
                        </span>
                      </span>
                    </a>
                  </li>
                {/each}
              </ol>
            {/if}

            {#if group.removals.length > 0}
              <h3 class="merge-queue__section-title">Removed without merging</h3>
              <ul class="merge-queue__rows" aria-label="Removed pull requests">
                {#each group.removals as pr (pr.ID)}
                  {@const removal = pr.merge_queue_removal!}
                  <li>
                    <a
                      class="merge-queue__row"
                      class:merge-queue__row--selected={isSelected(pr)}
                      class:multi-selected={selection.has(pr)}
                      aria-current={isSelected(pr) ? "true" : undefined}
                      href={withBasePath(buildMergeQueuePullRoute(routeRef(pr)))}
                      title={mergeQueueRemovalDescription(removal)}
                      onmousedown={preventShiftTextSelection}
                      onclick={(event) => handleRowClick(event, pr)}
                      oncontextmenu={(event) => openContextMenu(event, pr)}
                    >
                      <span class="merge-queue__position" aria-hidden="true"></span>
                      <span class="merge-queue__item">
                        <span class="merge-queue__line">
                          <span class="merge-queue__pr-title">{pr.Title}</span>
                          <span class="merge-queue__pr-number">#{pr.Number}</span>
                          {#if pr.workspace}<WorkspaceIndicator status={pr.workspace.status} size={12} />{/if}
                          {#if merging(pr)}
                            <span class="merge-queue__merging"><Spinner size={11} />Merging</span>
                          {/if}
                          {#if selection.has(pr)}<span class="kit-sr-only">, selected</span>{/if}
                        </span>
                        <span class="merge-queue__line">
                          <Chip size="sm" tone="danger">{mergeQueueRemovalReason(removal)}</Chip>
                          <span class="merge-queue__meta">
                            {authorLabel(pr)} ·
                            <time datetime={removal.removed_at} title={localDateTimeLabel(removal.removed_at)}
                              >{formatRelativeTime(removal.removed_at)}</time
                            >
                            {#if removal.failed_checks && removal.failed_checks.length > 0}
                              · {removal.failed_checks.map((check) => check.name).join(", ")}
                            {/if}
                          </span>
                        </span>
                      </span>
                    </a>
                  </li>
                {/each}
              </ul>
            {/if}
          </section>
        {/each}
      {/if}
    </div>
  </div>
{/snippet}

<ItemActionsMenu
  bind:open={contextMenuOpen}
  anchor={contextMenu?.anchor}
  returnFocus={contextMenu?.row}
  targets={contextMenu?.targets ?? []}
  onChanged={reloadAfterItemAction}
  selectionActive={selection.active}
  onClearSelection={() => selection.clear()}
/>

<style>
  .merge-queues {
    display: flex;
    flex: 1;
    flex-direction: column;
    min-height: 0;
  }

  .merge-queues__header {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-3) var(--space-2);
    border-bottom: var(--border-width) solid var(--border-muted);
  }

  .merge-queues__heading {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: var(--space-2);
  }

  .merge-queues__title {
    margin: 0;
    font-size: var(--font-size-md);
    font-weight: 600;
  }

  .merge-queues__summary {
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .merge-queues__toolbar {
    display: grid;
    grid-template-columns: minmax(0, 1fr) max-content max-content;
    gap: var(--space-2);
    align-items: center;
  }

  .merge-queues__body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
  }

  .merge-queues__state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-2);
    padding: var(--space-5);
    color: var(--text-muted);
  }

  .merge-queues__error {
    margin: 0;
    padding: var(--space-2) var(--space-3);
    color: var(--accent-red);
    font-size: var(--font-size-xs);
  }

  .merge-queue + .merge-queue {
    border-top: var(--border-width) solid var(--border-muted);
  }

  .merge-queue__header {
    position: sticky;
    top: 0;
    z-index: 1;
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-height: 32px;
    padding: 0 var(--space-3);
    background: var(--bg-surface);
    border-bottom: var(--border-width) solid var(--border-muted);
  }

  .merge-queue__title {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .merge-queue__repo {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .merge-queue__branch {
    flex: none;
    padding: 0 5px;
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-weight: 500;
  }

  .merge-queue__counts {
    margin-inline-start: auto;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    white-space: nowrap;
  }

  .merge-queue__provider-link {
    display: inline-flex;
    color: var(--text-muted);
  }

  .merge-queue__provider-link:hover {
    color: var(--text-primary);
  }

  .merge-queue__rows {
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .merge-queue__row {
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr);
    gap: var(--space-2);
    align-items: start;
    padding: var(--space-2) var(--space-3);
    color: var(--text-primary);
    text-decoration: none;
  }

  .merge-queue__row:hover {
    background: var(--sidebar-row-hover-bg, var(--bg-surface-hover));
  }

  .merge-queue__row--selected,
  .merge-queue__row--selected:hover {
    background: var(--bg-row-selected, var(--bg-inset));
  }

  .merge-queue__row.multi-selected,
  .merge-queue__row.multi-selected:hover {
    background: color-mix(in srgb, var(--accent-blue) 14%, var(--sidebar-row-bg, var(--bg-surface)));
  }

  .merge-queue__position {
    padding-top: 1px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-variant-numeric: tabular-nums;
    font-weight: 600;
    text-align: right;
  }

  .merge-queue__item {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    min-width: 0;
  }

  .merge-queue__line {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
  }

  .merge-queue__pr-title {
    overflow: hidden;
    font-size: var(--font-size-sm);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .merge-queue__pr-number {
    flex: none;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .merge-queue__merging {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: var(--space-1);
    color: var(--accent-purple);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    line-height: 1;
  }

  .merge-queue__meta {
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .merge-queue__section-title {
    margin: 0;
    padding: var(--space-2) var(--space-3) 0;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
    letter-spacing: var(--letter-spacing-label, 0.04em);
    text-transform: var(--label-transform, uppercase);
  }
</style>
