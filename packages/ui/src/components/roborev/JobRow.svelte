<script lang="ts">
  import type { components } from "../../api/roborev/generated/schema.js";
  import { timeAgo } from "../../utils/time.js";
  import StatusBadge from "./StatusBadge.svelte";
  import VerdictBadge from "./VerdictBadge.svelte";

  type ReviewJob = components["schemas"]["ReviewJob"];

  interface Props {
    job: ReviewJob;
    selected: boolean;
    highlighted: boolean;
    onclick: () => void;
  }
  let { job, selected, highlighted, onclick }: Props =
    $props();

  function formatElapsed(j: ReviewJob): string {
    if (!j.started_at) return "--";
    const start = new Date(j.started_at).getTime();
    const end = j.finished_at
      ? new Date(j.finished_at).getTime()
      : Date.now();
    const secs = Math.floor((end - start) / 1000);
    if (secs < 60) return `${secs}s`;
    const mins = Math.floor(secs / 60);
    const remSecs = secs % 60;
    if (mins < 60) return `${mins}m ${remSecs}s`;
    const hrs = Math.floor(mins / 60);
    const remMins = mins % 60;
    return `${hrs}h ${remMins}m`;
  }

  function formatCost(tokenUsage: string | undefined): string {
    if (!tokenUsage) return "--";
    try {
      const usage: unknown = JSON.parse(tokenUsage);
      if (
        typeof usage !== "object" ||
        usage === null ||
        !("has_cost" in usage) ||
        !("cost_usd" in usage) ||
        usage.has_cost !== true ||
        typeof usage.cost_usd !== "number"
      ) {
        return "--";
      }
      return `~$${usage.cost_usd.toFixed(2)}`;
    } catch {
      return "--";
    }
  }

  function shortRef(ref: string): string {
    if (ref.length > 10) return ref.slice(0, 8);
    return ref;
  }
</script>

<tr
  class="job-row"
  class:selected
  class:highlighted
  role="button"
  tabindex="0"
  onclick={onclick}
  onkeydown={(e) => {
    if (e.key === "Enter" || e.key === " ") onclick();
  }}
>
  <td class="col-id">
    <span class="mono">{job.id}</span>
  </td>
  <td class="col-ref">
    <span class="ref-group">
      {#if job.repo_name}
        <span class="repo-name">{job.repo_name}</span>
      {/if}
      {#if job.branch}
        <span class="branch-name">{job.branch}</span>
      {/if}
      <span class="git-ref mono" title={job.git_ref}>
        {shortRef(job.git_ref)}
      </span>
    </span>
    {#if job.commit_subject}
      <span class="commit-subject" title={job.commit_subject}>
        {job.commit_subject}
      </span>
    {/if}
  </td>
  <td class="col-agent">{job.agent}</td>
  <td class="col-status">
    <StatusBadge status={job.status} />
  </td>
  <td class="col-verdict">
    <VerdictBadge verdict={job.verdict} />
  </td>
  <td class="col-elapsed mono">
    {formatElapsed(job)}
  </td>
  <td class="col-cost mono">
    {formatCost(job.token_usage)}
  </td>
  <td class="col-type">{job.job_type}</td>
  <td class="col-queued" title={job.enqueued_at}>
    {timeAgo(job.enqueued_at)}
  </td>
</tr>

<style>
  .job-row {
    cursor: pointer;
    border-bottom: 1px solid var(--border-muted);
    transition: background 0.1s;
  }

  .job-row:hover {
    background: var(--bg-surface-hover);
  }

  .job-row.highlighted {
    background: color-mix(
      in srgb,
      var(--accent-blue) 4%,
      var(--bg-surface)
    );
    outline: 1px solid
      color-mix(
        in srgb,
        var(--accent-blue) 30%,
        transparent
      );
    outline-offset: -1px;
  }

  .job-row.selected {
    background: color-mix(
      in srgb,
      var(--accent-blue) 8%,
      var(--bg-surface)
    );
  }

  .job-row td {
    padding: 6px 10px;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    vertical-align: middle;
    white-space: nowrap;
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .col-id {
    width: 60px;
    color: var(--text-muted);
    text-align: right;
  }

  .col-ref {
    min-width: 160px;
    max-width: 300px;
    white-space: normal;
  }

  .ref-group {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
  }

  .repo-name {
    font-weight: 500;
    font-size: var(--font-size-sm);
  }

  .branch-name {
    color: var(--accent-purple);
    font-size: var(--font-size-xs);
  }

  .git-ref {
    color: var(--text-muted);
  }

  .commit-subject {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 280px;
  }

  .col-agent {
    max-width: 100px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .col-status {
    width: 90px;
  }

  .col-verdict {
    width: 70px;
  }

  .col-elapsed {
    width: 80px;
    color: var(--text-secondary);
    text-align: right;
  }

  .col-cost {
    width: 72px;
    color: var(--text-secondary);
    text-align: right;
  }

  .col-type {
    width: 80px;
    color: var(--text-secondary);
  }

  .col-queued {
    width: 80px;
    color: var(--text-muted);
    text-align: right;
  }
</style>
