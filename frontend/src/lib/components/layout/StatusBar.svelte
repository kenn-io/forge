<script lang="ts">
  import { getStores } from "@middleman/ui";
  import type { ActivityItem } from "@middleman/ui/api/types";
  import BudgetBars from "./BudgetBars.svelte";
  import BudgetPopover from "./BudgetPopover.svelte";
  import { client } from "../../api/runtime.js";
  import { getPage } from "../../stores/router.svelte.ts";

  const { activity, pulls, issues, sync } = getStores();

  let appVersion = $state("");

  $effect(() => {
    void client.GET("/version")
      .then(({ data }) => { if (data?.version) appVersion = data.version; })
      .catch(() => {});
  });

  let tick = $state(0);
  let tickHandle: ReturnType<typeof setInterval> | null = null;
  $effect(() => {
    tickHandle = setInterval(() => { tick++; }, 10_000);
    return () => { if (tickHandle !== null) clearInterval(tickHandle); };
  });

  function syncText(): string {
    void tick;
    const st = sync.getSyncState();
    if (st === null) return "";
    if (st.running) {
      if (st.progress) {
        return `syncing (${st.progress})`;
      }
      return "syncing\u2026";
    }
    if (!st.last_run_at) return "not synced";
    const diffMs = Date.now() - new Date(st.last_run_at).getTime();
    const mins = Math.floor(diffMs / 60_000);
    if (mins < 1) return "synced just now";
    if (mins < 60) return `synced ${mins}m ago`;
    return `synced ${Math.floor(mins / 60)}h ago`;
  }

  const openPulls = $derived(pulls.getPulls().filter((pr) => pr.State === "open"));
  const openIssues = $derived(issues.getIssues().filter((issue) => issue.State === "open"));

  interface RepoBackedItem {
    repo?: {
      provider?: string | undefined;
      platform_host?: string | undefined;
      repo_path?: string | undefined;
      owner?: string | undefined;
      name?: string | undefined;
    } | undefined;
    platform_host?: string | undefined;
    repo_owner?: string | undefined;
    repo_name?: string | undefined;
  }

  interface StatusCounts {
    pullRequests: number;
    issues: number;
    repos: number;
  }

  const BOT_SUFFIXES = ["[bot]", "-bot", "bot"];

  function repoKey(item: RepoBackedItem): string {
    const provider = item.repo?.provider ?? "";
    const platformHost = item.repo?.platform_host ?? item.platform_host ?? "";
    const repoPath = item.repo?.repo_path
      ?? [item.repo?.owner ?? item.repo_owner, item.repo?.name ?? item.repo_name]
        .filter(Boolean)
        .join("/");
    return `${provider}|${platformHost}/${repoPath}`;
  }

  function activityItemKey(item: ActivityItem): string {
    return `${repoKey(item)}|${item.item_type}|${item.item_number}`;
  }

  function isBot(author: string): boolean {
    const lower = author.toLowerCase();
    return BOT_SUFFIXES.some((suffix) => lower.endsWith(suffix));
  }

  function activityLifecycleState(item: ActivityItem): string {
    if (item.activity_type === "notification") {
      return item.subject_state || item.item_state;
    }
    return item.item_state;
  }

  const globalCounts = $derived.by((): StatusCounts => {
    const repos = new Set<string>();
    for (const pr of openPulls) repos.add(repoKey(pr));
    for (const issue of openIssues) repos.add(repoKey(issue));
    return {
      pullRequests: openPulls.length,
      issues: openIssues.length,
      repos: repos.size,
    };
  });

  const activityCounts = $derived.by((): StatusCounts => {
    const pullRequests = new Set<string>();
    const issueKeys = new Set<string>();
    const repos = new Set<string>();
    const itemFilter = activity.getItemFilter();
    const hideBots = activity.getHideBots();

    for (const item of activity.getActivityItems()) {
      if (hideBots && isBot(item.author)) continue;
      if (itemFilter === "prs" && item.item_type !== "pr") continue;
      if (itemFilter === "issues" && item.item_type !== "issue") continue;

      const lifecycleState = activityLifecycleState(item);
      const isOpenPullRequest = item.item_type === "pr"
        && lifecycleState === "open";
      const isOpenIssue = item.item_type === "issue"
        && lifecycleState === "open";

      if (isOpenPullRequest) {
        pullRequests.add(activityItemKey(item));
        repos.add(repoKey(item));
      } else if (isOpenIssue) {
        issueKeys.add(activityItemKey(item));
        repos.add(repoKey(item));
      }
    }

    return {
      pullRequests: pullRequests.size,
      issues: issueKeys.size,
      repos: repos.size,
    };
  });

  function isActivityStatusSurface(): boolean {
    const page = getPage();
    return page === "activity" || page === "mobile-activity";
  }

  const counts = $derived(isActivityStatusSurface() ? activityCounts : globalCounts);

  let popoverOpen = $state(false);

  function togglePopover() {
    popoverOpen = !popoverOpen;
  }

  function closePopover() {
    popoverOpen = false;
  }

  let rateLimitHosts = $derived.by(() => {
    void tick;
    return sync.getRateLimits();
  });
  let hasHosts = $derived(Object.keys(rateLimitHosts).length > 0);
</script>

<footer class="status-bar">
  <div class="status-left">
    <span class="status-item">{counts.pullRequests} PRs</span>
    <span class="status-sep">&middot;</span>
    <span class="status-item">{counts.issues} issues</span>
    <span class="status-sep">&middot;</span>
    <span class="status-item">{counts.repos} repos</span>
  </div>
  <div class="status-right">
    {#if hasHosts}
      <span class="budget-wrapper">
        <BudgetBars hosts={rateLimitHosts} onclick={togglePopover} expanded={popoverOpen} />
        {#if popoverOpen}
          <BudgetPopover hosts={rateLimitHosts} onclose={closePopover} />
        {/if}
      </span>
      <span class="status-sep">&middot;</span>
    {/if}
    {#if sync.getSyncState()?.last_error}
      <span class="status-item status-item--error" title={sync.getSyncState()?.last_error}>sync error</span>
      <span class="status-sep">&middot;</span>
    {/if}
    <span class="status-item" class:status-item--active={sync.getSyncState()?.running}>
      {#if sync.getSyncState()?.running}
        <span class="sync-dot"></span>
      {/if}
      {syncText()}
    </span>
    {#if appVersion}
      <span class="status-sep">&middot;</span>
      <span class="status-item status-item--version">{appVersion}</span>
    {/if}
  </div>
</footer>

<style>
  .status-bar {
    height: 24px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 12px;
    background: var(--bg-surface);
    border-top: 1px solid var(--border-muted);
    flex-shrink: 0;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
  }
  .status-left, .status-right {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .status-sep {
    color: var(--border-default);
  }
  .status-item--error {
    color: var(--accent-red);
  }
  .status-item--active {
    color: var(--accent-green);
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .sync-dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: var(--accent-green);
    animation: pulse 1.5s ease-in-out infinite;
  }
  .budget-wrapper {
    position: relative;
    display: flex;
    align-items: center;
  }
  @keyframes pulse {
    0%, 100% { opacity: 0.4; }
    50% { opacity: 1; }
  }
</style>
