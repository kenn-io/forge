<script lang="ts">
  import { untrack } from "svelte";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import { Button, Chip, EmptyState, IconButton, Spinner, formatRelativeTime, type ChipTone } from "@kenn-io/kit-ui";
  import type { PREvent } from "../../api/types.js";
  import type { PRRoborevTimeline } from "../../stores/roborev/pr-timeline.svelte.js";
  import { localDateTimeLabel } from "../../utils/time.js";
  import {
    buildRoborevTimeline,
    roborevVerdictLabel,
    roborevVerdictTone,
    type RoborevReviewState,
    type RoborevTimelineEntry,
  } from "./pr-review-timeline.js";

  interface Props {
    timeline: PRRoborevTimeline;
    events: ReadonlyArray<PREvent>;
    headSha: string;
    /** This node's workspace for the pull request; local reviews come from it. */
    workspaceId: string | null;
  }

  let { timeline, events, headSha, workspaceId }: Props = $props();

  // Showing the panel is the read: there is no polling while it stays open.
  $effect(() => {
    const id = workspaceId;
    untrack(() => timeline.load(id));
  });

  const entries = $derived(buildRoborevTimeline(events, timeline.getLocalJobs(), headSha));
  const unavailable = $derived(timeline.getUnavailable());
  const loadError = $derived(timeline.getLoadError());
  const loading = $derived(timeline.isLoading());

  const STATE_LABELS: Record<RoborevReviewState, string> = {
    current: "Current",
    superseded: "Superseded",
    addressed: "Addressed",
  };
  const STATE_TONES: Record<RoborevReviewState, ChipTone> = {
    current: "info",
    superseded: "muted",
    addressed: "success",
  };

  function isOpen(entry: RoborevTimelineEntry): boolean {
    return entry.source === "local"
      ? timeline.getSelectedJobId() === entry.jobId
      : timeline.getOpenProviderEntry()?.key === entry.key;
  }

  function open(entry: RoborevTimelineEntry): void {
    if (entry.source === "local") timeline.openLocal(entry.jobId);
    else timeline.openProvider(entry);
  }
</script>

<div class="roborev-timeline" data-testid="roborev-timeline">
  <div class="roborev-timeline__header">
    <span class="roborev-timeline__count">
      {entries.length} {entries.length === 1 ? "review" : "reviews"}
    </span>
    <IconButton
      size="sm"
      ariaLabel="Refresh Roborev reviews"
      title="Refresh Roborev reviews"
      disabled={loading}
      onclick={() => timeline.reload()}
    >
      {#if loading}
        <Spinner size={14} label="Loading Roborev reviews" />
      {:else}
        <RefreshCwIcon size={14} strokeWidth={2} aria-hidden="true" />
      {/if}
    </IconButton>
  </div>

  {#if entries.length === 0}
    {#if loading}
      <div class="roborev-timeline__loading"><Spinner size={14} label="Loading Roborev reviews" /> Loading reviews...</div>
    {:else if loadError}
      <EmptyState title={loadError} role="status">
        <Button size="sm" onclick={() => timeline.reload()}>Retry</Button>
      </EmptyState>
    {:else if unavailable === "daemon-unreachable"}
      <EmptyState title="Roborev daemon not reachable" description={timeline.getDaemonEndpoint()} role="status" />
    {:else if unavailable === "untracked"}
      <EmptyState title="No Roborev reviews" description="Roborev does not track this workspace's repository." role="status" />
    {:else}
      <EmptyState title="No Roborev reviews" role="status" />
    {/if}
  {:else}
    <ol class="roborev-timeline__list">
      {#each entries as entry (entry.key)}
        <li>
          <button
            type="button"
            class="roborev-timeline__row"
            class:roborev-timeline__row--open={isOpen(entry)}
            aria-current={isOpen(entry) ? "true" : undefined}
            onclick={() => open(entry)}
          >
            <time class="roborev-timeline__time" datetime={entry.at} title={localDateTimeLabel(entry.at)}>
              {formatRelativeTime(entry.at)}
            </time>
            <code class="roborev-timeline__commit">{entry.commit ? entry.commit.slice(0, 7) : "uncommitted"}</code>
            <Chip size="sm" tone={roborevVerdictTone(entry)}>{roborevVerdictLabel(entry)}</Chip>
            {#if entry.state}
              <Chip size="sm" tone={STATE_TONES[entry.state]}>{STATE_LABELS[entry.state]}</Chip>
            {/if}
            <span class="roborev-timeline__source">{entry.source === "local" ? "Local" : "CI"}</span>
          </button>
        </li>
      {/each}
    </ol>
    {#if loadError}
      <div class="roborev-timeline__note" role="status">
        {loadError}
        <Button size="sm" onclick={() => timeline.reload()}>Retry</Button>
      </div>
    {:else if unavailable === "daemon-unreachable"}
      <div class="roborev-timeline__note" role="status">Roborev daemon not reachable</div>
    {:else if timeline.isTruncated()}
      <div class="roborev-timeline__note">Older local reviews are not shown.</div>
    {/if}
  {/if}
</div>

<style>
  .roborev-timeline {
    flex-basis: 100%;
    width: 100%;
    min-width: 0;
    margin-top: 4px;
    display: flex;
    flex-direction: column;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    max-height: min(340px, 50vh);
    overflow: auto;
  }

  .roborev-timeline__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: 4px 8px 4px 12px;
    border-bottom: 1px solid var(--border-muted);
  }

  .roborev-timeline__count {
    font-size: var(--font-size-2xs);
    font-weight: 600;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .roborev-timeline__loading {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    padding: var(--space-6);
    color: var(--text-muted);
    font-size: var(--font-size-md);
  }

  .roborev-timeline__list {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .roborev-timeline__list li + li {
    border-top: 1px solid var(--border-muted);
  }

  .roborev-timeline__row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    width: 100%;
    min-height: 32px;
    padding: 4px 12px;
    background: none;
    border: none;
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
  }

  .roborev-timeline__row:hover,
  .roborev-timeline__row--open {
    background: var(--bg-surface-hover);
  }

  .roborev-timeline__row:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
  }

  .roborev-timeline__time {
    flex: 0 0 auto;
    min-width: 64px;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .roborev-timeline__commit {
    flex: 0 0 auto;
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  .roborev-timeline__source {
    margin-left: auto;
    flex: 0 0 auto;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
  }

  .roborev-timeline__note {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: 6px 12px;
    border-top: 1px solid var(--border-muted);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }
</style>
