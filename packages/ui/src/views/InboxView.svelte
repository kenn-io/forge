<script lang="ts">
  import { getStores, getNavigate } from "../context.js";
  import type { NotificationItem } from "../api/types.js";
  import type { NotificationSort, NotificationState } from "../stores/notifications.svelte.js";
  import { notificationDestination } from "../utils/notificationRoutes.js";

  type InboxFilters = {
    state?: NotificationState | undefined;
    reason?: string[] | undefined;
    type?: string[] | undefined;
    repo?: string | undefined;
    q?: string | undefined;
    sort?: string | undefined;
  };

  const {
    filters = {},
    onFiltersChange,
  }: {
    filters?: InboxFilters;
    onFiltersChange?: (filters: InboxFilters) => void;
  } = $props();

  const stores = getStores();
  const navigate = getNavigate();
  const inbox = stores.notifications;

  const stateOptions: Array<{ value: NotificationState; label: string }> = [
    { value: "unread", label: "Unread" },
    { value: "active", label: "Active" },
    { value: "read", label: "Read" },
    { value: "done", label: "Done" },
    { value: "all", label: "All" },
  ];
  const sortOptions: Array<{ value: NotificationSort; label: string }> = [
    { value: "priority", label: "Priority" },
    { value: "updated_desc", label: "Newest" },
    { value: "updated_asc", label: "Oldest" },
    { value: "repo", label: "Repository" },
  ];
  const validSorts = new Set<NotificationSort>(sortOptions.map((option) => option.value));

  type NormalizedInboxFilters = {
    state: NotificationState;
    reason: string[];
    type: string[];
    repo?: string | undefined;
    q?: string | undefined;
    sort: NotificationSort;
  };

  function normalizedFilters(raw: InboxFilters): NormalizedInboxFilters {
    const nextSort = raw.sort && validSorts.has(raw.sort as NotificationSort)
      ? raw.sort as NotificationSort
      : "priority";
    return {
      state: raw.state ?? "unread",
      reason: raw.reason ?? [],
      type: raw.type ?? [],
      ...(raw.repo && { repo: raw.repo }),
      ...(raw.q && { q: raw.q }),
      sort: nextSort,
    };
  }

  function filterKey(raw: InboxFilters): string {
    return JSON.stringify(normalizedFilters(raw));
  }

  function applyFilters(raw: InboxFilters): InboxFilters {
    const next = normalizedFilters(raw);
    inbox.setStateFilter(next.state);
    inbox.setReasonFilter(next.reason);
    inbox.setTypeFilter(next.type);
    inbox.setRepoFilter(next.repo);
    inbox.setSearchQuery(next.q);
    inbox.setSort(next.sort);
    return next;
  }

  let appliedRouteKey = "";
  $effect(() => {
    const key = filterKey(filters);
    if (key === appliedRouteKey) return;
    appliedRouteKey = key;
    applyFilters(filters);
    void inbox.loadNotifications();
  });

  function currentFilters(): InboxFilters {
    return {
      state: inbox.getStateFilter(),
      reason: inbox.getReasonFilter(),
      type: inbox.getTypeFilter(),
      ...(inbox.getRepoFilter() && { repo: inbox.getRepoFilter() }),
      ...(inbox.getSearchQuery() && { q: inbox.getSearchQuery() }),
      sort: inbox.getSort(),
    };
  }

  function updateFilters(next: Partial<InboxFilters>): void {
    const merged = normalizedFilters({ ...currentFilters(), ...next });
    appliedRouteKey = filterKey(merged);
    applyFilters(merged);
    onFiltersChange?.(merged);
    void inbox.loadNotifications();
  }

  function submitSearch(): void {
    updateFilters({ q: inbox.getSearchQuery() });
  }

  function reasonLabel(reason: string): string {
    return reason.replace(/_/g, " ");
  }

  function formatRelative(value: string): string {
    const t = Date.parse(value);
    if (Number.isNaN(t)) return value;
    const diff = Date.now() - t;
    const minute = 60 * 1000;
    const hour = 60 * minute;
    const day = 24 * hour;
    if (diff < hour) return `${Math.max(1, Math.floor(diff / minute))}m ago`;
    if (diff < day) return `${Math.floor(diff / hour)}h ago`;
    return `${Math.floor(diff / day)}d ago`;
  }

  function repoLabel(item: NotificationItem): string {
    return `${item.repo_owner}/${item.repo_name}`;
  }

  function itemLabel(item: NotificationItem): string {
    if (!item.item_number) return item.subject_type || item.item_type || "item";
    const prefix = item.item_type === "issue" ? "#" : "PR #";
    return `${prefix}${item.item_number}`;
  }

  function openItem(item: NotificationItem): void {
    const destination = notificationDestination(item);
    if (!destination) return;
    if (destination.kind === "internal") {
      navigate(destination.path);
      return;
    }
    window.open(destination.url, "_blank", "noopener");
  }

  function propagationStatus(item: NotificationItem): string {
    if (item.github_read_error === "max_attempts_exceeded") return "GitHub update stopped after repeated failures";
    if (item.github_read_error) return "GitHub update failed; will retry automatically";
    if (item.github_read_queued_at && !item.github_read_synced_at) return "GitHub update queued";
    return "";
  }

  function syncSuccessMessage(): string {
    const status = inbox.getSyncStatus();
    if (!status.last_finished_at || status.last_error) return "";
    return `Notifications synced ${formatRelative(status.last_finished_at)}.`;
  }

  function reasonOptions(): string[] {
    return [...new Set([...Object.keys(inbox.getSummary().by_reason), ...inbox.getReasonFilter()])].sort();
  }

  function repoOptions(): string[] {
    return [...new Set([...Object.keys(inbox.getSummary().by_repo), inbox.getRepoFilter()].filter(Boolean) as string[])].sort();
  }
</script>

<section class="inbox-page">
  <p class="draft-banner">Draft UI</p>
  <header class="inbox-header">
    <div>
      <p class="eyebrow">Inbox</p>
      <h1>GitHub notifications</h1>
      <p class="subtle">Unread notifications from monitored repositories, prioritized for mentions and review requests.</p>
    </div>
    <div class="summary-cards" aria-label="Inbox summary">
      <div class="summary-card"><span>Unread</span><strong>{inbox.getSummary().unread}</strong></div>
      <div class="summary-card"><span>Active</span><strong>{inbox.getSummary().total_active}</strong></div>
      <div class="summary-card"><span>Done</span><strong>{inbox.getSummary().done}</strong></div>
    </div>
  </header>

  <div class="toolbar">
    <div class="segmented" aria-label="Notification state">
      {#each stateOptions as option (option.value)}
        <button
          type="button"
          class:active={inbox.getStateFilter() === option.value}
          onclick={() => updateFilters({ state: option.value })}
        >{option.label}</button>
      {/each}
    </div>
    <input
      class="search-input"
      type="search"
      placeholder="Search title, repo, author, number"
      value={inbox.getSearchQuery() ?? ""}
      oninput={(event) => inbox.setSearchQuery((event.target as HTMLInputElement).value)}
      onchange={submitSearch}
      onkeydown={(event) => { if (event.key === "Enter") submitSearch(); }}
    />
    <select
      class="filter-select"
      aria-label="Notification reason"
      value={inbox.getReasonFilter()[0] ?? ""}
      onchange={(event) => {
        const value = (event.currentTarget as HTMLSelectElement).value;
        updateFilters({ reason: value ? [value] : [] });
      }}
    >
      <option value="">All reasons</option>
      {#each reasonOptions() as reason (reason)}
        <option value={reason}>{reasonLabel(reason)}</option>
      {/each}
    </select>
    <select
      class="filter-select"
      aria-label="Notification type"
      value={inbox.getTypeFilter()[0] ?? ""}
      onchange={(event) => {
        const value = (event.currentTarget as HTMLSelectElement).value;
        updateFilters({ type: value ? [value] : [] });
      }}
    >
      <option value="">All types</option>
      <option value="pr">Pull requests</option>
      <option value="issue">Issues</option>
      <option value="release">Releases</option>
      <option value="commit">Commits</option>
      <option value="other">Other</option>
    </select>
    <select
      class="filter-select"
      aria-label="Notification repository"
      value={inbox.getRepoFilter() ?? ""}
      onchange={(event) => {
        const value = (event.currentTarget as HTMLSelectElement).value;
        updateFilters({ repo: value || undefined });
      }}
    >
      <option value="">All repositories</option>
      {#each repoOptions() as repo (repo)}
        <option value={repo}>{repo}</option>
      {/each}
    </select>
    <select
      class="filter-select"
      aria-label="Notification sort"
      value={inbox.getSort()}
      onchange={(event) => updateFilters({ sort: (event.currentTarget as HTMLSelectElement).value })}
    >
      {#each sortOptions as option (option.value)}
        <option value={option.value}>{option.label}</option>
      {/each}
    </select>
    <button type="button" class="action-btn" onclick={() => { onFiltersChange?.(currentFilters()); void inbox.loadNotifications(); }} disabled={inbox.isLoading()}>
      Refresh
    </button>
    <button type="button" class="action-btn" onclick={() => void inbox.triggerSync()} disabled={inbox.isSyncRunning()}>
      {inbox.isSyncRunning() ? "Syncing..." : "Sync notifications"}
    </button>
  </div>

  {#if syncSuccessMessage()}
    <div class="notice success">{syncSuccessMessage()}</div>
  {/if}

  <div class="bulk-bar" class:visible={inbox.getSelectedCount() > 0}>
    <span>{inbox.getSelectedCount()} selected</span>
    <button type="button" onclick={() => void inbox.markSelectedDone()} disabled={inbox.isActionInFlight()}>Done</button>
    <button type="button" onclick={() => void inbox.markSelectedRead()} disabled={inbox.isActionInFlight()}>Mark read</button>
    <button type="button" onclick={() => void inbox.markSelectedUndone()} disabled={inbox.isActionInFlight()}>Undone</button>
    <button type="button" onclick={inbox.clearSelection}>Clear</button>
  </div>

  {#if inbox.getError()}
    <div class="notice error">{inbox.getError()}</div>
  {/if}

  <div class="list-card">
    <div class="list-head">
      <label class="select-all">
        <input
          type="checkbox"
          checked={inbox.getNotifications().length > 0 && inbox.getSelectedCount() === inbox.getNotifications().length}
          onchange={(event) => (event.currentTarget as HTMLInputElement).checked ? inbox.selectVisible() : inbox.clearSelection()}
        />
        Select visible
      </label>
      <span>{inbox.getNotifications().length} notifications</span>
    </div>

    {#if inbox.isLoading()}
      <div class="empty">Loading inbox...</div>
    {:else if inbox.getNotifications().length === 0}
      <div class="empty">No notifications match this view.</div>
    {:else}
      <ul class="notification-list">
        {#each inbox.getNotifications() as item (item.id)}
          {@const destination = notificationDestination(item)}
          <li class="notification-row" class:unread={item.unread}>
            <input
              type="checkbox"
              aria-label={`Select ${item.subject_title}`}
              checked={inbox.getSelectedIDs().has(item.id)}
              onchange={() => inbox.toggleSelected(item.id)}
            />
            <button
              class="row-main"
              type="button"
              disabled={!destination}
              title={destination ? undefined : "Link unavailable"}
              onclick={() => openItem(item)}
            >
              <div class="row-title">
                <span class="reason">{reasonLabel(item.reason)}</span>
                <strong>{item.subject_title}</strong>
              </div>
              <div class="row-meta">
                <span>{repoLabel(item)}</span>
                <span>{itemLabel(item)}</span>
                {#if item.item_author}<span>@{item.item_author}</span>{/if}
                <span>{formatRelative(item.github_updated_at)}</span>
                {#if propagationStatus(item)}<span class="queued">{propagationStatus(item)}</span>{/if}
                {#if !destination}<span class="queued">Link unavailable</span>{/if}
              </div>
            </button>
            {#if item.web_url}
              <a class="external-link" href={item.web_url} target="_blank" rel="noreferrer">GitHub</a>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</section>

<style>
  .inbox-page {
    flex: 1;
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    color: var(--text-primary);
    container-type: inline-size;
  }

  .draft-banner {
    flex-shrink: 0;
    margin: 0;
    padding: 6px 16px;
    border-bottom: 1px solid var(--border-default);
    background: var(--warning-bg, var(--bg-surface));
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-weight: 600;
    letter-spacing: 0.02em;
    text-transform: uppercase;
  }

  .inbox-header {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .eyebrow { display: none; }

  h1 {
    margin: 0;
    font-size: var(--font-size-md);
    font-weight: 600;
  }

  .subtle {
    margin: 2px 0 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .summary-cards {
    display: flex;
    gap: 12px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    white-space: nowrap;
  }

  .summary-card {
    display: flex;
    align-items: baseline;
    gap: 5px;
  }

  .summary-card span { color: var(--text-muted); }
  .summary-card strong { color: var(--text-primary); font-size: var(--font-size-md); }

  .toolbar {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 16px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .segmented {
    display: flex;
    align-items: center;
    gap: 1px;
    background: var(--bg-inset);
    border-radius: var(--radius-sm);
    padding: 2px;
  }

  .segmented button {
    padding: 2px 7px;
    border-radius: calc(var(--radius-sm) - 1px);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 500;
    transition: background 0.12s, color 0.12s;
  }

  .segmented button.active {
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: var(--shadow-sm);
  }

  .segmented button:hover:not(.active) { color: var(--text-secondary); }

  .search-input {
    margin-left: auto;
    width: 200px;
    border: 1px solid var(--border-default);
    background: var(--bg-primary);
    color: var(--text-primary);
    border-radius: var(--radius-sm);
    padding: 3px 7px;
    font-size: var(--font-size-sm);
  }

  .filter-select {
    border: 1px solid var(--border-default);
    background: var(--bg-primary);
    color: var(--text-primary);
    border-radius: var(--radius-sm);
    padding: 3px 7px;
    font-size: var(--font-size-sm);
    max-width: 150px;
  }

  .action-btn,
  .bulk-bar button {
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    padding: 4px 8px;
    font-size: var(--font-size-sm);
  }

  .action-btn:hover,
  .bulk-bar button:hover {
    border-color: var(--border-default);
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .bulk-bar {
    flex-shrink: 0;
    display: none;
    align-items: center;
    gap: 8px;
    padding: 6px 16px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .bulk-bar.visible { display: flex; }

  .notice {
    flex-shrink: 0;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border-default);
    font-size: var(--font-size-sm);
  }

  .notice.error {
    background: color-mix(in srgb, var(--accent-red) 10%, transparent);
    color: var(--accent-red);
  }

  .notice.success {
    background: color-mix(in srgb, var(--accent-green) 10%, transparent);
    color: var(--accent-green);
  }

  .list-card {
    flex: 1;
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    background: var(--bg-primary);
  }

  .list-head {
    flex-shrink: 0;
    display: flex;
    justify-content: space-between;
    align-items: center;
    color: var(--text-muted);
    padding: 6px 16px;
    border-bottom: 1px solid var(--border-default);
    font-size: var(--font-size-xs);
  }

  .select-all { display: flex; align-items: center; gap: 8px; }

  .notification-list {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    list-style: none;
    margin: 0;
    padding: 0 16px;
  }

  .notification-row {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 12px;
    min-height: 62px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-muted);
  }

  .notification-row:last-child { border-bottom: 0; }

  .notification-row:hover { background: var(--bg-surface-hover); }

  .notification-row.unread {
    background: color-mix(in srgb, var(--accent-blue) 8%, transparent);
    box-shadow: inset 3px 0 0 var(--accent-blue);
  }

  .row-main {
    display: flex;
    min-width: 0;
    flex-direction: column;
    align-items: stretch;
    gap: 3px;
    text-align: left;
    color: inherit;
    cursor: pointer;
  }

  .row-title { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .row-title strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--font-size-sm); font-weight: 500; }

  .reason {
    display: inline-block;
    flex-shrink: 0;
    padding: 1px 5px;
    border-radius: 3px;
    background: color-mix(in srgb, var(--accent-green) 15%, transparent);
    color: var(--accent-green);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    letter-spacing: 0.3px;
    text-transform: uppercase;
    white-space: nowrap;
  }

  .row-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    flex-wrap: wrap;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .queued { color: var(--accent-amber); }

  .external-link {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-decoration: none;
  }

  .external-link:hover { color: var(--accent-blue); }

  .empty { padding: 40px; text-align: center; color: var(--text-muted); font-size: var(--font-size-md); }

  @media (max-height: 240px) {
    .draft-banner,
    .inbox-header,
    .toolbar,
    .list-head {
      padding-block: 2px;
    }

    .subtle,
    .summary-cards {
      display: none;
    }
  }

  @container (max-width: 760px) {
    .inbox-header,
    .toolbar {
      align-items: stretch;
      flex-wrap: wrap;
      gap: 8px;
      padding-inline: 8px;
    }

    .summary-cards { order: 2; }
    .segmented { flex: 1 1 100%; }
    .segmented button { flex: 1; padding-inline: 6px; }
    .search-input { order: -1; flex: 1 0 100%; width: 100%; margin-left: 0; }
    .notification-list { padding: 0; }
  }
</style>
