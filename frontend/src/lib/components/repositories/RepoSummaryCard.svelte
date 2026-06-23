<script lang="ts">
  import { ActionButton, Chip, operationGate } from "@middleman/ui";
  import { timeAgo } from "@middleman/ui/utils/time";
  import { ExternalLinkIcon, FolderTreeIcon } from "../../icons.js";
  import ProviderIcon from "../provider/ProviderIcon.svelte";
  import RepoMetricGrid from "./RepoMetricGrid.svelte";
  import {
    displayReleaseName,
    isStaleRelease,
    localDateTimeLabel,
    repoKey,
    repoStateKey,
    shouldShowPlatformHost,
    type RepoMetric,
    type RepoSummaryCard,
  } from "./repoSummary.js";

  type TimelinePoint = {
    id: string;
    type: "commit" | "release";
    label: string;
    detail: string;
    date: string;
    tone: "green" | "amber" | "red" | "blue";
  };

  interface Props {
    summary: RepoSummaryCard;
    showProviderIcon?: boolean;
    onviewprs: () => void;
    onviewissues: () => void;
    onviewrepo: () => void;
    onopencomposer: () => void;
    onopenissue: (number: number) => void;
  }

  let {
    summary,
    showProviderIcon = false,
    onviewprs,
    onviewissues,
    onviewrepo,
    onopencomposer,
    onopenissue,
  }: Props = $props();

  const key = $derived(repoKey(summary));
  const stateKey = $derived(repoStateKey(summary));
  const repoURL = $derived(
    `https://${summary.platform_host}/${summary.owner}/${summary.name}`,
  );
  const showPlatformHost = $derived(shouldShowPlatformHost(summary));
  const syncTime = $derived(
    summary.last_sync_completed_at
      || summary.last_sync_started_at,
  );
  const release = $derived(summary.latest_release);
  const releases = $derived(
    summary.releases.length > 0
      ? summary.releases
      : release ? [release] : [],
  );
  const oldestRelease = $derived(
    releases.length > 0 ? releases[releases.length - 1] : undefined,
  );
  const staleRelease = $derived(isStaleRelease(summary));
  const releaseDate = $derived(release?.published_at);
  const releaseLabel = $derived(displayReleaseName(release));
  const releaseStatus = $derived.by(() => {
    if (!release) return "No release";
    if (staleRelease) return "Stale";
    return release.prerelease ? "Pre-release" : "Latest";
  });
  const releaseStatusClass = $derived.by(() => {
    if (!release) return "chip--muted";
    if (staleRelease) return "chip--red";
    return release.prerelease ? "chip--amber" : "chip--green";
  });
  let cardElement: HTMLElement | undefined = $state();
  let hoveredTimelinePoint = $state<TimelinePoint | null>(null);
  let stickyTimelinePoint = $state<TimelinePoint | null>(null);
  const activeTimelinePoint = $derived(
    stickyTimelinePoint ?? hoveredTimelinePoint,
  );
  const timelinePoints = $derived.by(() => {
    const releasePoints: TimelinePoint[] = releases
      .filter((item) => item.published_at)
      .map((item) => ({
        id: `release-${item.tag_name}`,
        type: "release" as const,
        label: item.tag_name || item.name || "Release",
        detail: item.name || item.tag_name || "Release",
        date: item.published_at ?? "",
        tone: item.prerelease ? "amber" : "blue",
      }));
    const commitPoints: TimelinePoint[] = summary.commit_timeline.map((point) => ({
      id: `commit-${point.sha}`,
      type: "commit" as const,
      label: shortSHA(point.sha),
      detail: point.message || "Commit",
      date: point.committed_at,
      tone: staleRelease ? "red" : release?.prerelease ? "amber" : "green",
    }));
    return [...commitPoints, ...releasePoints].sort(
      (a, b) => new Date(a.date).getTime() - new Date(b.date).getTime(),
    );
  });
  const timelineStart = $derived.by(() => {
    const firstPoint = timelinePoints[0];
    return firstPoint?.date ?? releaseDate;
  });
  const timelineEnd = $derived.by(() => {
    const lastPoint = timelinePoints[timelinePoints.length - 1];
    return lastPoint?.date;
  });
  const metrics = $derived<RepoMetric[]>([
    {
      label: "Open PRs",
      value: summary.open_pr_count,
      tone: "blue",
      onclick: onviewprs,
    },
    {
      label: "Open issues",
      value: summary.open_issue_count,
      tone: "green",
      onclick: onviewissues,
    },
    {
      label: "Draft PRs",
      value: summary.draft_pr_count,
      tone: "amber",
    },
    {
      label: "Cached PRs",
      value: summary.cached_pr_count,
    },
    {
      label: "Cached issues",
      value: summary.cached_issue_count,
    },
  ]);

  function timelinePosition(date: string): number {
    if (!timelineStart) return 50;
    const start = new Date(timelineStart).getTime();
    const end = Math.max(Date.now(), timelineEnd ? new Date(timelineEnd).getTime() : 0);
    const current = new Date(date).getTime();
    if (!Number.isFinite(start) || !Number.isFinite(current) || end <= start) {
      return 50;
    }
    const pct = ((current - start) / (end - start)) * 100;
    return Math.max(1, Math.min(99, pct));
  }

  function shortSHA(sha: string): string {
    return sha.slice(0, 7);
  }

  function avatarURL(author: string): string {
    const login = encodeURIComponent(author.trim());
    if (login === "") return "";
    const host = summary.platform_host;
    return `https://${host}/${login}.png?size=40`;
  }

  function pinTimelinePoint(event: MouseEvent, point: TimelinePoint): void {
    event.stopPropagation();
    stickyTimelinePoint = stickyTimelinePoint?.id === point.id ? null : point;
    hoveredTimelinePoint = point;
  }

  function handleDocumentClick(event: MouseEvent): void {
    if (!stickyTimelinePoint) return;
    if (event.target instanceof Node && cardElement?.contains(event.target)) {
      return;
    }
    stickyTimelinePoint = null;
    hoveredTimelinePoint = null;
  }
</script>

<svelte:document onclick={handleDocumentClick} />

<article class="repo-card" aria-labelledby={`repo-${stateKey}`} bind:this={cardElement}>
  <div class="repo-card__header">
      <div class="repo-card__identity">
      <div class="repo-card__name-row">
        {#if showProviderIcon}
          <ProviderIcon
            provider={summary.repo.provider}
            size={16}
            class="repo-card__provider-icon"
          />
        {/if}
        <button
          id={`repo-${stateKey}`}
          class="repo-card__name"
          onclick={onviewprs}
        >
          <span>{summary.owner}</span>
          <span class="repo-card__slash">/</span>
          <span>{summary.name}</span>
        </button>
        <a
          class="repo-card__gh-link"
          href={repoURL}
          target="_blank"
          rel="noopener noreferrer"
          title={`Open on ${summary.platform_host}`}
          aria-label={`Open ${summary.owner}/${summary.name} on ${summary.platform_host}`}
        >
          <ExternalLinkIcon
            size="14"
            strokeWidth="2"
            aria-hidden="true"
          />
        </a>
      </div>
      {#if showPlatformHost}
        <Chip size="sm" class="chip--muted" uppercase={false}>
          {summary.platform_host}
        </Chip>
      {/if}
    </div>

    <div class="repo-card__actions">
      <ActionButton
        size="sm"
        tone="neutral"
        surface="outline"
        ariaLabel={`View repository source for ${summary.owner} ${summary.name} on ${summary.platform_host}`}
        onclick={onviewrepo}
      >
        <FolderTreeIcon size="14" strokeWidth="2" aria-hidden="true" />
        View repo
      </ActionButton>
      {#if summary.repo.capabilities.issue_mutation}
        {@const createIssueGate = operationGate(summary.operations?.create_issue)}
        <ActionButton
          size="sm"
          tone="neutral"
          surface="outline"
          disabled={createIssueGate.unavailable}
          title={createIssueGate.unavailable ? createIssueGate.reason : undefined}
          onclick={onopencomposer}
        >
          New issue
        </ActionButton>
      {/if}
    </div>
  </div>

  <RepoMetricGrid {metrics} compact />

  <section class="repo-card__release" aria-label="Latest release">
    <div class="repo-card__release-head">
      <span>Latest release</span>
      {#if summary.commits_since_release !== undefined}
        <strong>
          {summary.commits_since_release} {summary.commits_since_release === 1 ? "commit" : "commits"}
        </strong>
      {/if}
    </div>

    <div class="repo-card__release-meta">
      <Chip size="md" class="chip--release" uppercase={false}>
        {releaseLabel}
      </Chip>
      <Chip
        size="sm"
        class={releaseStatusClass}
        uppercase={false}
      >
        {releaseStatus}
      </Chip>
      {#if releaseDate}
        <span title={localDateTimeLabel(releaseDate)}>
          {timeAgo(releaseDate)}
        </span>
      {/if}
      {#if summary.commits_since_release !== undefined}
        <span class="repo-card__commits-copy">since release</span>
      {/if}
    </div>

    <div class="repo-card__timeline">
      <div class="repo-card__timeline-track">
        {#each timelinePoints as point (point.id)}
          <button
            type="button"
            class={[
              "repo-card__timeline-point",
              `repo-card__timeline-point--${point.type}`,
              `repo-card__timeline-point--${point.tone}`,
              {
                "repo-card__timeline-point--active": activeTimelinePoint?.id === point.id,
              },
            ]}
            style={`--x: ${timelinePosition(point.date)}%;`}
            title={`${point.label}: ${point.detail}`}
            aria-label={`${point.type === "release" ? "Release" : "Commit"} ${point.label}`}
            onmouseenter={() => hoveredTimelinePoint = point}
            onmouseleave={() => hoveredTimelinePoint = null}
            onclick={(event) => pinTimelinePoint(event, point)}
          ></button>
        {/each}
        {#if activeTimelinePoint}
          <div
            class={[
              "repo-card__timeline-popover",
              {
                "repo-card__timeline-popover--sticky":
                  stickyTimelinePoint?.id === activeTimelinePoint.id,
              },
            ]}
            style={`--x: ${timelinePosition(activeTimelinePoint.date)}%;`}
          >
            <strong>{activeTimelinePoint.label}</strong>
            <span>{activeTimelinePoint.detail}</span>
            <time datetime={activeTimelinePoint.date}>
              {localDateTimeLabel(activeTimelinePoint.date)}
            </time>
          </div>
        {/if}
      </div>
      <div class="repo-card__timeline-labels">
        <span>{oldestRelease?.tag_name ?? "Release"}</span>
        <span>Now</span>
      </div>
    </div>
  </section>

  <section class="repo-card__issues" aria-label="Recent open issues">
    <h2>Recent open issues</h2>
    {#if summary.recent_issues.length > 0}
      <div class="repo-card__issue-list">
        {#each summary.recent_issues as issue (issue.number)}
          <button
            class="repo-card__issue-row"
            onclick={() => onopenissue(issue.number)}
          >
            <span class="repo-card__issue-main">
              <strong>#{issue.number}</strong>
              <span>{issue.title}</span>
            </span>
            <span class="repo-card__issue-meta">
              <img
                class="repo-card__avatar"
                src={avatarURL(issue.author)}
                alt=""
                title={issue.author}
                loading="lazy"
              />
              <span>{timeAgo(issue.last_activity_at)}</span>
            </span>
          </button>
        {/each}
      </div>
    {:else}
      <p class="repo-card__empty-note">No open issues in cache.</p>
    {/if}
  </section>

  <footer class="repo-card__footer">
    <span
      class={[
        "repo-card__status",
        { "repo-card__status--error": summary.last_sync_error },
      ]}
    >
      {summary.last_sync_error ? "Sync issue" : "Active"}
    </span>
    {#if syncTime}
      <span title={localDateTimeLabel(syncTime)}>
        Synced {timeAgo(syncTime)}
      </span>
    {:else}
      <span>Not synced yet</span>
    {/if}
  </footer>
</article>

<style>
  .repo-card {
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-surface);
    box-shadow: var(--shadow-sm);
  }

  .repo-card__header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 14px;
    padding: 14px 14px 10px;
  }

  .repo-card__identity {
    min-width: 0;
    display: grid;
    gap: 4px;
    justify-items: start;
  }

  .repo-card__name-row,
  .repo-card__actions,
  .repo-card__release-meta,
  .repo-card__footer,
  .repo-card__issue-meta {
    display: flex;
    align-items: center;
  }

  .repo-card__name-row {
    min-width: 0;
    gap: 6px;
    color: var(--text-muted);
  }

  :global(.repo-card__provider-icon) {
    color: var(--text-secondary);
  }

  .repo-card__name {
    min-width: 0;
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    font-weight: 700;
    line-height: 1.25;
    overflow-wrap: anywhere;
  }

  .repo-card__name:hover {
    color: var(--accent-blue);
  }

  .repo-card__gh-link {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    color: var(--text-muted);
    transition: color 0.1s;
  }

  .repo-card__gh-link:hover {
    color: var(--accent-blue);
    text-decoration: none;
  }

  .repo-card__slash {
    padding: 0 4px;
    color: var(--text-muted);
    font-weight: 500;
  }

  .repo-card__actions {
    flex-shrink: 0;
    gap: 8px;
  }

  .repo-card__release {
    display: grid;
    gap: 8px;
    padding: 12px 14px 10px;
    border-bottom: 1px solid var(--border-muted);
  }

  .repo-card__release-head {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    line-height: 1.2;
  }

  .repo-card__release-head strong {
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    text-align: right;
  }

  .repo-card__release-meta {
    flex-wrap: wrap;
    gap: 8px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .repo-card__commits-copy {
    margin-left: -4px;
    color: var(--text-muted);
  }

  .repo-card__timeline {
    display: grid;
    gap: 4px;
  }

  .repo-card__timeline-track {
    position: relative;
    height: 34px;
  }

  .repo-card__timeline-track::before {
    content: "";
    position: absolute;
    top: 22px;
    right: 0;
    left: 0;
    height: 2px;
    border-radius: 999px;
    background: var(--border-muted);
  }

  .repo-card__timeline-point {
    position: absolute;
    top: 18px;
    left: var(--x);
    z-index: 2;
    width: 8px;
    height: 8px;
    padding: 0;
    border: 0;
    border-radius: 50%;
    background: var(--accent-green);
    cursor: pointer;
    transform: translateX(-50%);
    box-shadow: 0 0 0 2px var(--bg-surface);
  }

  .repo-card__timeline-point--release {
    top: 16px;
    z-index: 3;
    width: 12px;
    height: 12px;
    box-shadow:
      0 0 0 2px var(--bg-surface),
      0 0 0 3px var(--border-default);
  }

  .repo-card__timeline-point--blue {
    background: var(--accent-blue);
  }

  .repo-card__timeline-point--amber {
    background: var(--accent-amber);
  }

  .repo-card__timeline-point--red {
    background: var(--accent-red);
  }

  .repo-card__timeline-point--green {
    background: var(--accent-green);
  }

  .repo-card__timeline-point--active,
  .repo-card__timeline-point:hover {
    z-index: 4;
    box-shadow:
      0 0 0 2px var(--bg-surface),
      0 0 0 5px color-mix(in srgb, var(--accent-blue) 18%, transparent);
  }

  .repo-card__timeline-popover {
    position: absolute;
    bottom: 30px;
    left: clamp(104px, var(--x), calc(100% - 104px));
    z-index: 5;
    display: grid;
    width: max-content;
    min-width: 160px;
    max-width: min(240px, calc(100% - 16px));
    gap: 3px;
    padding: 8px 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    box-shadow: var(--shadow-md);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1.3;
    pointer-events: none;
    transform: translateX(-50%);
  }

  .repo-card__timeline-popover--sticky {
    pointer-events: auto;
  }

  .repo-card__timeline-popover strong {
    color: var(--accent-blue);
    font-size: var(--font-size-sm);
    overflow-wrap: anywhere;
  }

  .repo-card__timeline-popover span {
    color: var(--text-primary);
    overflow-wrap: anywhere;
  }

  .repo-card__timeline-popover time {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .repo-card__timeline-labels {
    display: flex;
    justify-content: space-between;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .repo-card__issues {
    display: grid;
    gap: 8px;
    padding: 10px 14px 12px;
    min-height: 82px;
  }

  .repo-card__issues h2 {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 500;
  }

  .repo-card__issue-list {
    display: grid;
    gap: 4px;
  }

  .repo-card__issue-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 12px;
    min-height: 24px;
    text-align: left;
  }

  .repo-card__issue-row:hover .repo-card__issue-main span {
    color: var(--accent-blue);
  }

  .repo-card__issue-main {
    min-width: 0;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: baseline;
    gap: 8px;
    color: var(--text-primary);
  }

  .repo-card__issue-main strong {
    flex-shrink: 0;
    color: var(--accent-blue);
    font-weight: 600;
  }

  .repo-card__issue-main span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .repo-card__issue-meta {
    flex-shrink: 0;
    gap: 8px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .repo-card__avatar {
    width: 18px;
    height: 18px;
    border: 1px solid var(--border-default);
    border-radius: 50%;
    object-fit: cover;
    background: var(--bg-inset);
  }

  .repo-card__empty-note {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .repo-card__footer {
    gap: 10px;
    padding: 9px 14px;
    border-top: 1px solid var(--border-muted);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .repo-card__status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--accent-green);
    font-weight: 600;
  }

  .repo-card__status::before {
    content: "";
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: currentColor;
  }

  .repo-card__status--error {
    color: var(--accent-red);
  }

  :global(.chip--release) {
    background: color-mix(in srgb, var(--accent-blue) 13%, transparent);
    color: var(--accent-blue);
  }

  @media (max-width: 700px) {
    .repo-card__header {
      flex-direction: column;
    }

    .repo-card__actions {
      width: 100%;
      justify-content: space-between;
    }

    .repo-card__issue-row {
      grid-template-columns: minmax(0, 1fr);
      justify-items: start;
      gap: 5px;
    }

    .repo-card__issue-main span {
      white-space: normal;
    }
  }
</style>
