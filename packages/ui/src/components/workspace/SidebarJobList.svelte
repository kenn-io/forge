<script lang="ts">
  import { getStores } from "../../context.js";
  import VerdictBadge from "../roborev/VerdictBadge.svelte";
  import type {
    components,
  } from "../../api/roborev/generated/schema.js";

  type ReviewJob = components["schemas"]["ReviewJob"];

  const stores = getStores();

  const jobs = $derived(
    stores.roborevJobs?.getJobs() ?? [],
  );
  const loading = $derived(
    stores.roborevJobs?.isLoading() ?? false,
  );
  const selectedJobId = $derived(
    stores.roborevJobs?.getSelectedJobId(),
  );

  function timeAgo(iso: string): string {
    const ms = Date.now() - new Date(iso).getTime();
    const sec = Math.floor(ms / 1000);
    if (sec < 60) return `${sec}s`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h`;
    const d = Math.floor(hr / 24);
    return `${d}d`;
  }

  function shortRef(ref: string): string {
    return ref.length > 8 ? ref.slice(0, 8) : ref;
  }

  function handleClick(job: ReviewJob): void {
    if (selectedJobId === job.id) {
      stores.roborevJobs?.deselectJob();
    } else {
      stores.roborevJobs?.selectJob(job.id);
    }
  }
</script>

<div class="sidebar-job-list">
  {#if loading && jobs.length === 0}
    <div class="list-empty">Loading reviews...</div>
  {:else if jobs.length === 0}
    <div class="list-empty">No reviews found</div>
  {:else}
    {#each jobs as job (job.id)}
      <button
        class="job-row"
        class:selected={selectedJobId === job.id}
        onclick={() => handleClick(job)}
      >
        <div class="row-top">
          <span
            class="job-dot status-{job.status}"
            title={job.status}
          ></span>
          <span class="job-id">#{job.id}</span>
          <span class="job-type">{job.job_type}</span>
          <span class="job-time">
            {timeAgo(job.enqueued_at)}
          </span>
        </div>
        <div class="row-bottom">
          {#if job.commit_subject}
            <span class="commit-subject">
              {job.commit_subject}
            </span>
          {:else}
            <span class="commit-ref">
              {shortRef(job.git_ref)}
            </span>
          {/if}
          {#if job.status === "done" || job.verdict}
            <VerdictBadge verdict={job.verdict} />
          {/if}
        </div>
      </button>
    {/each}
  {/if}
</div>

<style>
  .sidebar-job-list {
    display: flex;
    flex-direction: column;
    overflow-y: auto;
    flex: 1;
  }

  .list-empty {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: 1;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    padding: 24px;
  }

  .job-row {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    padding: 8px 12px;
    border: none;
    border-bottom: 1px solid var(--border-muted);
    background: none;
    text-align: left;
    cursor: pointer;
    color: var(--text-primary);
    font-family: inherit;
    font-size: var(--font-size-sm);
    width: 100%;
  }

  .job-row:hover {
    background: var(--bg-surface-hover);
  }

  .job-row.selected {
    background: color-mix(
      in srgb, var(--accent-blue) 10%, transparent
    );
  }

  .row-top {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .job-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .job-dot.status-queued { background: var(--review-queued); }
  .job-dot.status-running { background: var(--review-running); }
  .job-dot.status-done { background: var(--review-done); }
  .job-dot.status-failed { background: var(--review-failed); }
  .job-dot.status-canceled { background: var(--review-canceled); }

  .job-id {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .job-type {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .job-time {
    margin-left: auto;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .row-bottom {
    display: flex;
    align-items: center;
    gap: 6px;
    padding-left: 12px;
  }

  .commit-subject {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1;
    min-width: 0;
  }

  .commit-ref {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }
</style>
