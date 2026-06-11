<script lang="ts">
  import { onDestroy } from "svelte";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import ChevronUpIcon from "@lucide/svelte/icons/chevron-up";
  import { relativeTime, shortDate } from "../../api/dates.js";
  import type { KataTaskAPI, KataTaskSummary } from "../../api/kata/taskTypes.js";
  import type { KataCurrentView } from "../../stores/kata-workspace.svelte.js";
  import {
    DEFAULT_KATA_TASK_SORT,
    sortKataTasks,
    toggleKataTaskSort,
    type KataTaskSort,
    type KataTaskSortKey,
  } from "../../features/kata/taskSort.js";

  interface Props {
    currentView: KataCurrentView;
    scopeLabel?: string;
    scopedProjectName?: string | null;
    selectedIssueUID?: string | null;
    loading?: boolean;
    resetGeneration?: number;
    navigationGeneration?: number;
    api?: KataTaskAPI;
    onSelect: (issue: KataTaskSummary) => void;
  }

  let {
    currentView,
    scopeLabel = undefined,
    scopedProjectName = null,
    selectedIssueUID = null,
    loading = false,
    resetGeneration = 0,
    navigationGeneration = 0,
    api = undefined,
    onSelect,
  }: Props = $props();

  const SORT_STORAGE_KEY = "middleman:kata:issue-sort/v1";
  let sort: KataTaskSort = $state(loadSort());

  let expanded: Record<string, boolean> = $state({});
  let childrenByUID: Record<string, KataTaskSummary[]> = $state({});
  let loadingChildren: Record<string, boolean> = $state({});
  let tableBody: HTMLDivElement | null = $state(null);
  let childLoadGeneration = 0;
  let lastResetGeneration = $state<number | null>(null);

  // When the user is scoped to a single project, server-side groupings
  // like "Today / This Evening" feel like noise — they're a kata
  // today-bucket detail that doesn't carry inside a project view.
  // Collapse to a flat list and let the sort drive the order.
  let isProjectScoped = $derived(Boolean(scopedProjectName));
  let flatIssues = $derived(
    isProjectScoped ? topLevelIssues(currentView.groups.flatMap((group) => group.issues)) : [],
  );

  // For the Today view, the kata daemon hands us a "This evening"
  // sub-bucket alongside "Today". That bucket is a daemon-side
  // concept that confuses users coming from Things-style apps, so we
  // merge it into the Today group on the way to the renderer. The
  // raw group data is untouched — purely a visual collapse, and the
  // merge only runs on the Today view so a future daemon-provided
  // "evening" bucket in any other view passes through unchanged.
  let visibleGroups = $derived.by(() => {
    if (isProjectScoped) return [];
    const groups = currentView.groups.map((group) => ({ ...group, issues: [...group.issues] }));
    if (currentView.name === "today") {
      const todayIdx = groups.findIndex((group) => group.id === "today");
      const eveningIdx = groups.findIndex((group) => group.id === "evening");
      if (eveningIdx >= 0) {
        if (todayIdx >= 0) {
          groups[todayIdx]!.issues.push(...groups[eveningIdx]!.issues);
          groups.splice(eveningIdx, 1);
        } else {
          groups[eveningIdx] = { ...groups[eveningIdx]!, id: "today", title: "Today" };
        }
      }
    }
    const allIssues = groups.flatMap((group) => group.issues);
    return groups
      .map((group) => ({ ...group, issues: topLevelIssues(group.issues, allIssues) }))
      .filter((group) => group.issues.length > 0);
  });

  // Sorting by "updated" collapses multi-group views (e.g. Today's
  // Overdue/Today buckets) into one global list so the order isn't reset
  // per group. A single-group view (like Inbox) has nothing to collapse,
  // so keep its labeled region instead of dropping it to a bare list.
  let shouldFlatten = $derived(!isProjectScoped && sort.key === "updated" && visibleGroups.length > 1);
  let globalSortedIssues = $derived(
    shouldFlatten
      ? sortKataTasks(topLevelIssues(currentView.groups.flatMap((group) => group.issues)), sort)
      : [],
  );

  function loadSort(): KataTaskSort {
    if (typeof window === "undefined") return DEFAULT_KATA_TASK_SORT;
    try {
      const raw = window.localStorage.getItem(SORT_STORAGE_KEY);
      if (!raw) return DEFAULT_KATA_TASK_SORT;
      const parsed = JSON.parse(raw) as Partial<KataTaskSort>;
      const validKeys: KataTaskSortKey[] = ["priority", "title", "updated", "owner", "id"];
      if (
        parsed.key && validKeys.includes(parsed.key) &&
        (parsed.direction === "asc" || parsed.direction === "desc")
      ) {
        return { key: parsed.key, direction: parsed.direction };
      }
    } catch {
      // Corrupt — fall back to defaults silently.
    }
    return DEFAULT_KATA_TASK_SORT;
  }

  function persistSort(next: KataTaskSort) {
    if (typeof window === "undefined") return;
    try {
      window.localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify(next));
    } catch {
      // Storage unavailable — best-effort.
    }
  }

  function handleSortClick(key: KataTaskSortKey) {
    sort = toggleKataTaskSort(sort, key);
    persistSort(sort);
  }

  function viewTitle(view: KataCurrentView): string {
    if (scopeLabel) return scopeLabel;
    return view.name.charAt(0).toUpperCase() + view.name.slice(1);
  }

  function totalIssues(view: KataCurrentView): number {
    return view.groups.reduce((sum, group) => sum + group.issues.length, 0);
  }

  function totalVisibleIssues(): number {
    if (isProjectScoped) return flatIssues.length;
    if (shouldFlatten) return globalSortedIssues.length;
    return visibleGroups.reduce((sum, group) => sum + group.issues.length, 0);
  }

  function isSelected(issue: KataTaskSummary): boolean {
    return selectedIssueUID === issue.uid;
  }

  function hasChildren(issue: KataTaskSummary): boolean {
    return (issue.child_counts?.total ?? 0) > 0;
  }

  function priorityLabel(priority: number | undefined): string | null {
    if (priority === undefined) return null;
    return `P${priority}`;
  }

  function displayId(issue: KataTaskSummary): string {
    // Inside a project the project prefix on every row is noise — show
    // just the short id. At the top level we keep the qualified form
    // so the project is identifiable at a glance.
    return isProjectScoped ? issue.short_id : issue.qualified_id;
  }

  function issueHierarchyKey(issue: KataTaskSummary): string {
    return `${issue.project_uid}:${issue.short_id}`;
  }

  function parentHierarchyKey(issue: KataTaskSummary): string | null {
    return issue.parent_short_id ? `${issue.project_uid}:${issue.parent_short_id}` : null;
  }

  function topLevelIssues(
    issues: readonly KataTaskSummary[],
    allIssues: readonly KataTaskSummary[] = issues,
  ): KataTaskSummary[] {
    const visibleKeys = new Set(allIssues.map(issueHierarchyKey));
    return issues.filter((issue) => {
      const parentKey = parentHierarchyKey(issue);
      return parentKey === null || !visibleKeys.has(parentKey);
    });
  }

  async function toggleExpand(issue: KataTaskSummary, event: MouseEvent | KeyboardEvent) {
    event.stopPropagation();
    const uid = issue.uid;
    const currentlyExpanded = expanded[uid] === true;
    expanded = { ...expanded, [uid]: !currentlyExpanded };
    if (!currentlyExpanded && !childrenByUID[uid] && api) {
      const generation = childLoadGeneration;
      loadingChildren = { ...loadingChildren, [uid]: true };
      try {
        const detail = await api.issue(uid);
        if (generation !== childLoadGeneration || !findIssueByUID(uid)) return;
        childrenByUID = { ...childrenByUID, [uid]: detail.children ?? [] };
      } finally {
        if (generation === childLoadGeneration) {
          loadingChildren = { ...loadingChildren, [uid]: false };
        }
      }
    }
  }

  function sortIndicator(key: KataTaskSortKey): "asc" | "desc" | null {
    return sort.key === key ? sort.direction : null;
  }

  function sortLabel(key: KataTaskSortKey, label: string): string {
    const ind = sortIndicator(key);
    if (ind === "asc") return `Sort by ${label}, currently ascending`;
    if (ind === "desc") return `Sort by ${label}, currently descending`;
    return `Sort by ${label}`;
  }

  // Keyboard navigation selects on every step, and each selection kicks
  // off a detail + events fetch upstream. Selection therefore only
  // commits once the keyboard settles: 50ms after the last navigation
  // keydown, and never while a navigation key is still physically held.
  // The held-key gate matters because OS key-repeat intervals are often
  // longer than any reasonable debounce — a timer alone would still
  // commit one fetch per repeated row. Focus itself moves instantly;
  // only the upstream notification waits for the cursor to settle.
  const KEYBOARD_SELECT_DEBOUNCE_MS = 50;
  let keyboardSelectTimer: ReturnType<typeof setTimeout> | undefined;
  let pendingKeyboardSelectUID: string | null = null;
  // Tracked by event.code (physical key), not event.key: "G" is Shift+g,
  // so releasing Shift before g would make keydown record "G" but keyup
  // report "g", stranding the entry and blocking selection until blur.
  const heldNavKeys = new Set<string>();

  function cancelPendingKeyboardSelect() {
    pendingKeyboardSelectUID = null;
    if (keyboardSelectTimer !== undefined) {
      clearTimeout(keyboardSelectTimer);
      keyboardSelectTimer = undefined;
    }
  }

  onDestroy(cancelPendingKeyboardSelect);

  function selectNow(issue: KataTaskSummary) {
    cancelPendingKeyboardSelect();
    onSelect(issue);
  }

  function restartKeyboardSelectTimer() {
    if (keyboardSelectTimer !== undefined) clearTimeout(keyboardSelectTimer);
    keyboardSelectTimer = setTimeout(() => {
      keyboardSelectTimer = undefined;
      // A navigation key is still held: stay pending. The matching keyup
      // restarts the timer, so even a slow OS key-repeat never commits
      // intermediate rows mid-hold.
      if (heldNavKeys.size > 0) return;
      commitKeyboardSelect();
    }, KEYBOARD_SELECT_DEBOUNCE_MS);
  }

  function commitKeyboardSelect() {
    const uid = pendingKeyboardSelectUID;
    pendingKeyboardSelectUID = null;
    if (!uid) return;
    // Re-resolve at commit time: a live refresh inside the settle window
    // can drop the row, and selecting a vanished issue would surface an
    // error for something the user can no longer see.
    const issue = findIssueByUID(uid);
    if (issue) onSelect(issue);
  }

  function focusRow(target: HTMLElement | null) {
    if (!target) return;
    target.focus();
    const uid = target.dataset.uid;
    if (!uid || !findIssueByUID(uid)) return;
    pendingKeyboardSelectUID = uid;
    restartKeyboardSelectTimer();
  }

  // Window-level so a release outside the table (focus moved mid-hold)
  // can't strand a key in the held set and block selection forever.
  function handleWindowKeyup(event: KeyboardEvent) {
    if (!heldNavKeys.delete(event.code)) return;
    if (heldNavKeys.size === 0 && pendingKeyboardSelectUID !== null && keyboardSelectTimer === undefined) {
      restartKeyboardSelectTimer();
    }
  }

  // Keyups are lost entirely when the window loses focus mid-hold; treat
  // blur as releasing everything so the pending selection still settles.
  function handleWindowBlur() {
    heldNavKeys.clear();
    if (pendingKeyboardSelectUID !== null && keyboardSelectTimer === undefined) {
      restartKeyboardSelectTimer();
    }
  }

  function handleListKeydown(event: KeyboardEvent) {
    const target = event.target;
    if (!(target instanceof HTMLElement) || !target.classList.contains("row")) return;
    if (!tableBody) return;
    if (event.metaKey || event.ctrlKey || event.altKey) return;

    const rows = Array.from(tableBody.querySelectorAll<HTMLElement>("button.row"));
    const idx = rows.indexOf(target);
    if (idx === -1) return;

    let nextIdx: number | null = null;
    switch (event.key) {
      case "ArrowDown":
      case "j":
        nextIdx = Math.min(rows.length - 1, idx + 1);
        break;
      case "ArrowUp":
      case "k":
        nextIdx = Math.max(0, idx - 1);
        break;
      case "Home":
      case "g":
        nextIdx = 0;
        break;
      case "End":
      case "G":
        nextIdx = rows.length - 1;
        break;
      case "ArrowRight": {
        const uid = target.dataset.uid;
        if (uid && expanded[uid] !== true) {
          const issue = findIssueByUID(uid);
          if (issue && hasChildren(issue)) {
            event.preventDefault();
            void toggleExpand(issue, event);
          }
        }
        return;
      }
      case "ArrowLeft": {
        const uid = target.dataset.uid;
        if (uid && expanded[uid] === true) {
          const issue = findIssueByUID(uid);
          if (issue) {
            event.preventDefault();
            void toggleExpand(issue, event);
          }
        }
        return;
      }
      default:
        return;
    }
    event.preventDefault();
    heldNavKeys.add(event.code);
    // Boundary keys (j on last, k on first, Home/End at the edge) can
    // resolve to the row already focused; skip the re-focus so we
    // don't double-fire the click handler and refetch the same issue.
    if (nextIdx === idx) return;
    focusRow(rows[nextIdx] ?? null);
  }

  function findIssueByUID(uid: string): KataTaskSummary | undefined {
    for (const group of currentView.groups) {
      const match = group.issues.find((issue) => issue.uid === uid);
      if (match) return match;
    }
    for (const children of Object.values(childrenByUID)) {
      const match = children.find((issue) => issue.uid === uid);
      if (match) return match;
    }
    return undefined;
  }

  $effect(() => {
    if (lastResetGeneration === null) {
      lastResetGeneration = resetGeneration;
      return;
    }
    if (resetGeneration === lastResetGeneration) return;
    lastResetGeneration = resetGeneration;
    childLoadGeneration += 1;
    expanded = {};
    childrenByUID = {};
    loadingChildren = {};
  });

  // A pending keyboard selection dies the moment the workspace starts
  // any navigation (view/scope/route/daemon change). Waiting for the
  // post-load list remount is too late: a held key released while the
  // new view's data is still in flight would commit against the old
  // view and its onSelect would supersede the in-progress navigation.
  let lastNavigationGeneration: number | null = null;
  $effect(() => {
    if (lastNavigationGeneration !== null && navigationGeneration !== lastNavigationGeneration) {
      cancelPendingKeyboardSelect();
    }
    lastNavigationGeneration = navigationGeneration;
  });
</script>

<svelte:window onkeyup={handleWindowKeyup} onblur={handleWindowBlur} />

<main class="issue-list" aria-label="Issues">
  <div class="pane-header">
    <div class="heading">
      <h2>{viewTitle(currentView)}</h2>
      <span class="count">{totalVisibleIssues()} {totalVisibleIssues() === 1 ? "task" : "tasks"}</span>
    </div>
    {#if loading}
      <span class="sr-only" aria-live="polite">Loading snapshot</span>
    {/if}
  </div>

  <div class="table" class:table--project-scoped={isProjectScoped}>
    <!-- The table-body is the keyboard-nav root: any row inside this
         container is reachable via ↓/↑ (j/k), Home/End (g/G), and
         ↔ to expand or collapse subtasks. -->
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="table-body" bind:this={tableBody} onkeydown={handleListKeydown}>
      <div class="table-header" role="row">
        <button
          class="col col-id"
          type="button"
          aria-label={sortLabel("id", "ID")}
          aria-pressed={sortIndicator("id") !== null}
          onclick={() => handleSortClick("id")}
        >
          <span>ID</span>
          {#if sortIndicator("id") === "asc"}
            <ChevronUpIcon size={11} strokeWidth={2} />
          {:else if sortIndicator("id") === "desc"}
            <ChevronDownIcon size={11} strokeWidth={2} />
          {/if}
        </button>
        <button
          class="col col-title"
          type="button"
          aria-label={sortLabel("title", "Title")}
          aria-pressed={sortIndicator("title") !== null}
          onclick={() => handleSortClick("title")}
        >
          <!-- Spacer matches the chevron column in body rows so the
               "Title" label aligns with the title text underneath. -->
          <span class="chevron chevron--placeholder" aria-hidden="true"></span>
          <span>Title</span>
          {#if sortIndicator("title") === "asc"}
            <ChevronUpIcon size={11} strokeWidth={2} />
          {:else if sortIndicator("title") === "desc"}
            <ChevronDownIcon size={11} strokeWidth={2} />
          {/if}
        </button>
        <button
          class="col col-updated"
          type="button"
          aria-label={sortLabel("updated", "Updated")}
          aria-pressed={sortIndicator("updated") !== null}
          onclick={() => handleSortClick("updated")}
        >
          <span>Updated</span>
          {#if sortIndicator("updated") === "asc"}
            <ChevronUpIcon size={11} strokeWidth={2} />
          {:else if sortIndicator("updated") === "desc"}
            <ChevronDownIcon size={11} strokeWidth={2} />
          {/if}
        </button>
        <button
          class="col col-priority"
          type="button"
          aria-label={sortLabel("priority", "Priority")}
          aria-pressed={sortIndicator("priority") !== null}
          onclick={() => handleSortClick("priority")}
        >
          <span>Priority</span>
          {#if sortIndicator("priority") === "asc"}
            <ChevronUpIcon size={11} strokeWidth={2} />
          {:else if sortIndicator("priority") === "desc"}
            <ChevronDownIcon size={11} strokeWidth={2} />
          {/if}
        </button>
        <span class="col col-due col-static">Due</span>
        <button
          class="col col-owner"
          type="button"
          aria-label={sortLabel("owner", "Owner")}
          aria-pressed={sortIndicator("owner") !== null}
          onclick={() => handleSortClick("owner")}
        >
          <span>Owner</span>
          {#if sortIndicator("owner") === "asc"}
            <ChevronUpIcon size={11} strokeWidth={2} />
          {:else if sortIndicator("owner") === "desc"}
            <ChevronDownIcon size={11} strokeWidth={2} />
          {/if}
        </button>
        <span class="col col-tags col-static">Tags</span>
      </div>

      {#if totalVisibleIssues() === 0}
        <div class="empty">No tasks</div>
      {/if}

      {#if isProjectScoped}
        {#each sortKataTasks(flatIssues, sort) as issue (issue.uid)}
          {@render row(issue)}
        {/each}
      {:else if shouldFlatten}
        {#each globalSortedIssues as issue (issue.uid)}
          {@render row(issue)}
        {/each}
      {:else}
        {#each visibleGroups as group (group.id)}
          <section class="group" aria-labelledby={`group-${group.id}`}>
            <h3 class="group-title" id={`group-${group.id}`}>
              <span>{group.title}</span>
              <span class="group-count">{group.issues.length}</span>
            </h3>
            {#each sortKataTasks(group.issues, sort) as issue (issue.uid)}
              {@render row(issue)}
            {/each}
          </section>
        {/each}
      {/if}
    </div>
  </div>
</main>

{#snippet row(issue: KataTaskSummary)}
  {@const priority = priorityLabel(issue.priority)}
  {@const labels = issue.labels?.join(" · ") ?? ""}
  {@const expandable = hasChildren(issue)}
  {@const isExpanded = expanded[issue.uid] === true}
  <button
    class="row issue-row"
    class:selected={isSelected(issue)}
    aria-current={isSelected(issue) ? "true" : undefined}
    aria-expanded={expandable ? isExpanded : undefined}
    data-uid={issue.uid}
    onclick={() => selectNow(issue)}
  >
    <span class="cell cell-id"><span class="id-badge">{displayId(issue)}</span></span>
    <span class="cell cell-title">
      {#if expandable}
        <!-- A span (not a button) inside the row's outer <button> — nesting
             real interactives is invalid HTML. Clicks still toggle expand
             via the onclick handler; keyboard equivalents are ArrowRight
             and ArrowLeft handled at the table level. -->
        <span
          class="chevron"
          class:open={isExpanded}
          aria-hidden="true"
          onclick={(event) => toggleExpand(issue, event)}
        >
          <ChevronRightIcon size={12} strokeWidth={2} />
        </span>
      {:else}
        <span class="chevron chevron--placeholder" aria-hidden="true"></span>
      {/if}
      <span class="title-text">{issue.title}</span>
    </span>
    <span class="cell cell-updated" title={issue.updated_at}>
      {relativeTime(issue.updated_at)}
    </span>
    <span class="cell cell-priority">
      {#if priority}
        <span class={`pri-pill priority-${issue.priority}`}>{priority}</span>
      {/if}
    </span>
    <span class="cell cell-due" title={issue.metadata.deadline_on ?? ""}>
      {#if issue.metadata.deadline_on}{shortDate(issue.metadata.deadline_on)}{/if}
    </span>
    <span class="cell cell-owner">{issue.owner ?? ""}</span>
    <span class="cell cell-tags" title={labels}>
      {#if labels}{labels}{/if}
    </span>
    <span class="sr-keywords">
      <span>project: {issue.project_name}</span>
      {#if issue.metadata.deadline_on}<span> · Due {shortDate(issue.metadata.deadline_on)}</span>{/if}
      {#if issue.owner}<span> · owner: {issue.owner}</span>{/if}
      {#if issue.priority !== undefined}<span> · priority: {issue.priority}</span>{/if}
    </span>
  </button>

  {#if isExpanded}
    {#if loadingChildren[issue.uid]}
      <div class="children-status">Loading subtasks…</div>
    {:else if (childrenByUID[issue.uid] ?? []).length === 0}
      <div class="children-status">No subtasks.</div>
    {:else}
      {#each childrenByUID[issue.uid] ?? [] as child (child.uid)}
        {@const childPriority = priorityLabel(child.priority)}
        {@const childLabels = child.labels?.join(" · ") ?? ""}
        <button
          class="row issue-row row--child"
          class:selected={isSelected(child)}
          aria-current={isSelected(child) ? "true" : undefined}
          data-uid={child.uid}
          onclick={() => selectNow(child)}
        >
          <span class="cell cell-id"><span class="id-badge">{displayId(child)}</span></span>
          <span class="cell cell-title">
            <span class="chevron chevron--placeholder" aria-hidden="true"></span>
            <span class="title-text">{child.title}</span>
          </span>
          <span class="cell cell-updated" title={child.updated_at}>
            {relativeTime(child.updated_at)}
          </span>
          <span class="cell cell-priority">
            {#if childPriority}
              <span class={`pri-pill priority-${child.priority}`}>{childPriority}</span>
            {/if}
          </span>
          <span class="cell cell-due" title={child.metadata.deadline_on ?? ""}>
            {#if child.metadata.deadline_on}{shortDate(child.metadata.deadline_on)}{/if}
          </span>
          <span class="cell cell-owner">{child.owner ?? ""}</span>
          <span class="cell cell-tags" title={childLabels}>
            {#if childLabels}{childLabels}{/if}
          </span>
          <span class="sr-keywords">
            <span>project: {child.project_name}</span>
            {#if child.metadata.deadline_on}<span> · Due {shortDate(child.metadata.deadline_on)}</span>{/if}
            {#if child.owner}<span> · owner: {child.owner}</span>{/if}
            {#if child.priority !== undefined}<span> · priority: {child.priority}</span>{/if}
          </span>
        </button>
      {/each}
    {/if}
  {/if}
{/snippet}

<style>
  .issue-list {
    min-width: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    background: var(--bg-primary);
    /* Establish a container so column visibility can respond to the
       pane's own width — the horizontal-split layout can leave the list
       under 400px even when the viewport is huge, and a viewport-based
       media query would miss that. */
    container-type: inline-size;
    container-name: list;
  }

  .pane-header {
    position: relative;
    flex-shrink: 0;
    padding: 10px 16px 8px;
    padding-right: 96px;
    background: var(--bg-surface);
    border-bottom: 1px solid var(--border-default);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .heading {
    display: flex;
    align-items: baseline;
    gap: 10px;
  }

  .pane-header h2 {
    font-size: var(--font-size-xl);
    line-height: 1.1;
    font-weight: 600;
    letter-spacing: -0.01em;
  }

  .count {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  .table {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    /* Shared grid template — header and rows live in the same scroll
       plane and inherit this template, so horizontal movement stays
       aligned in split views. The ID column uses a fixed ch-based
       width per scope so every row (and the header) start the title
       at the same x; max-content was being re-evaluated per row,
       which let different IDs leave different gaps. The two widths
       are tight enough for short_ids at top level / qualified_ids in
       a project to fit without truncation. The title takes the
       leftover via `1fr` so the metadata cluster anchors at the
       right edge with no whitespace pocket. */
    --table-id-col: 13ch;         /* room for "Finances#rent" + badge padding */
    --table-cols:
      var(--table-id-col)         /* id badge */
      minmax(220px, 1fr)          /* title — absorbs leftover space */
      minmax(64px, 80px)          /* updated */
      minmax(68px, 80px)          /* priority — wide enough that the
                                     uppercase header doesn't overflow
                                     into UPDATED's sort chevron */
      minmax(56px, 70px)          /* due */
      minmax(72px, 110px)         /* owner */
      minmax(96px, 200px);        /* tags */
    --table-gap: 14px;
    --table-min-width: 720px;
  }

  .table.table--project-scoped {
    /* Short ids inside a project are typically 4–6 chars so the
       column can collapse and give the title more room. The badge
       around each id adds 7px of padding on both sides, so include
       14px here — otherwise a 6-char id clips against the badge
       background. */
    --table-id-col: calc(6ch + 14px);
  }

  .table-header {
    position: sticky;
    top: 0;
    z-index: 3;
    display: grid;
    grid-template-columns: var(--table-cols);
    gap: var(--table-gap);
    width: 100%;
    min-width: var(--table-min-width);
    padding: 5px 6px;
    align-items: center;
    background: var(--bg-surface);
    border-bottom: 1px solid var(--border-default);
    color: var(--text-faint);
    font-size: var(--font-size-3xs);
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .col {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    letter-spacing: inherit;
    text-transform: inherit;
    text-align: left;
    cursor: pointer;
    border-radius: var(--radius-sm);
    transition: color 0.1s;
  }

  .col:hover,
  .col:focus-visible {
    color: var(--text-primary);
  }

  .col-static {
    cursor: default;
  }

  .col-static:hover {
    color: inherit;
  }

  .col-priority,
  .col-due,
  .col-updated {
    justify-content: flex-end;
    text-align: right;
  }

  .col-updated,
  .col-due {
    font-variant-numeric: tabular-nums;
  }

  .table-body {
    flex: 1;
    overflow: auto;
    padding: 0 6px 12px;
  }

  .empty {
    padding: 32px 12px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-align: center;
  }

  .group {
    margin-top: 6px;
  }

  .group:first-child {
    margin-top: 0;
  }

  .group-title {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 8px 4px;
    color: var(--text-secondary);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    letter-spacing: 0.05em;
    text-transform: uppercase;
    border-top: 1px solid var(--border-muted);
    margin-top: 4px;
  }

  .group:first-child .group-title {
    border-top: 0;
    margin-top: 0;
  }

  .group-count {
    color: var(--text-faint);
    font-variant-numeric: tabular-nums;
    text-transform: none;
    letter-spacing: 0;
    font-weight: 500;
  }

  .row {
    width: 100%;
    min-width: var(--table-min-width);
    display: grid;
    grid-template-columns: var(--table-cols);
    gap: var(--table-gap);
    align-items: center;
    padding: 3px 6px;
    border-radius: var(--radius-sm);
    text-align: left;
    border: 0;
    background: transparent;
    color: inherit;
    min-height: 26px;
    transition: background 0.08s;
  }

  .row:hover {
    background: var(--bg-surface-hover);
  }

  .row.selected {
    background: color-mix(in srgb, var(--accent-blue) 20%, var(--bg-primary));
    box-shadow:
      inset 3px 0 0 var(--accent-blue),
      inset 0 0 0 1px color-mix(in srgb, var(--accent-blue) 24%, transparent);
    color: var(--text-primary);
  }

  .row:focus-visible {
    outline: none;
    background: var(--accent-blue-soft);
  }

  .cell {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  .cell-id {
    display: inline-flex;
    align-items: center;
  }

  .id-badge {
    display: inline-flex;
    align-items: center;
    height: 18px;
    padding: 0 7px;
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    font-weight: 500;
    font-variant-numeric: tabular-nums;
    letter-spacing: 0.01em;
  }

  .row.selected .id-badge,
  .row:focus-visible .id-badge {
    background: color-mix(in srgb, var(--accent-blue) 28%, transparent);
    color: var(--accent-blue);
  }

  .row.selected .title-text {
    font-weight: 600;
  }

  .cell-title {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    white-space: normal;
    overflow-wrap: anywhere;
    word-break: break-word;
  }

  .title-text {
    flex: 1;
    min-width: 0;
    line-height: 1.35;
    /* Cap at two lines so a runaway title can't push the row off the
       viewport; longer titles ellipsize in the second line. */
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .chevron {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 14px;
    height: 14px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    transition: transform 0.1s, background 0.1s, color 0.1s;
  }

  .chevron:hover:not(.chevron--placeholder) {
    background: var(--bg-inset);
    color: var(--text-primary);
  }

  .chevron.open {
    transform: rotate(90deg);
    color: var(--accent-blue);
  }

  .chevron--placeholder {
    pointer-events: none;
  }

  .cell-priority {
    display: inline-flex;
    justify-content: flex-end;
    align-items: center;
  }

  .pri-pill {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 24px;
    height: 17px;
    padding: 0 6px;
    border-radius: var(--radius-sm);
    background: var(--accent-amber-soft);
    color: var(--accent-amber);
    font-size: var(--font-size-3xs);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .priority-0 {
    background: var(--accent-red-soft);
    color: var(--accent-red);
  }

  /* Every metadata cell holds at the base 12px set on .cell — only
     the title (13px) climbs above the row baseline. The old setup mixed
     11/12/11 across tags/owner/due so each row read as a hand-laid
     collage instead of one stride of metadata. Color carries the
     hierarchy: muted for low-signal columns, secondary for owner,
     primary for the title. */
  .cell-tags {
    color: var(--text-muted);
  }

  .cell-owner {
    color: var(--text-secondary);
  }

  .cell-due,
  .cell-updated {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    text-align: right;
  }

  .row--child .cell-title {
    padding-left: 18px;
  }

  .children-status {
    padding: 4px 14px 4px 32px;
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    font-style: italic;
  }

  .sr-keywords {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    clip-path: inset(50%);
    white-space: nowrap;
  }

  /* Pane-width-driven column visibility. The list narrows whenever the
     user picks the side-by-side layout and drags the list pane down to
     its minimum; viewport queries miss this because the *window* is
     still wide. Tags + Owner drop first (low scanning value), then
     Due, leaving the irreducible quartet of ID / title / updated /
     priority that you need to triage a row. */
  @container list (max-width: 880px) {
    .col-tags,
    .cell-tags {
      display: none;
    }

    .table {
      --table-cols:
        var(--table-id-col)
        minmax(220px, 1fr)
        minmax(64px, 80px)
        minmax(68px, 80px)
        minmax(56px, 70px)
        minmax(72px, 110px);
      --table-min-width: 580px;
    }
  }

  @container list (max-width: 680px) {
    .col-owner,
    .cell-owner {
      display: none;
    }

    .table {
      --table-cols:
        var(--table-id-col)
        minmax(180px, 1fr)
        minmax(60px, 76px)
        minmax(64px, 78px)
        minmax(54px, 68px);
      --table-gap: 12px;
      --table-min-width: 460px;
    }
  }

  @container list (max-width: 520px) {
    .col-due,
    .cell-due {
      display: none;
    }

    .table {
      --table-cols:
        var(--table-id-col)
        minmax(140px, 1fr)
        minmax(58px, 72px)
        minmax(62px, 74px);
      --table-gap: 10px;
      --table-min-width: 320px;
    }
  }
</style>
