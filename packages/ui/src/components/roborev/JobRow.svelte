<script lang="ts">
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import type { components } from "../../api/roborev/generated/schema.js";
  import {
    panelCostUsd,
    panelElapsedStart,
    panelStatusLabel,
  } from "../../utils/roborev-panel.js";
  import { formatRelativeTime } from "@kenn-io/kit-ui";
  import StatusBadge from "./StatusBadge.svelte";
  import VerdictBadge from "./VerdictBadge.svelte";

  type ReviewJob = components["schemas"]["ReviewJob"];

  interface Props {
    job: ReviewJob;
    selected: boolean;
    highlighted: boolean;
    onclick: () => void;
    members?: ReviewJob[] | undefined;
    member?: boolean;
    lastMember?: boolean;
    expandable?: boolean;
    expanded?: boolean;
    ontoggle?: (() => void) | undefined;
  }
  let {
    job,
    selected,
    highlighted,
    onclick,
    members,
    member = false,
    lastMember = false,
    expandable = false,
    expanded = false,
    ontoggle,
  }: Props = $props();

  const panelStatus = $derived(panelStatusLabel(job));

  function formatElapsed(j: ReviewJob): string {
    const startedAt = panelElapsedStart(j, members);
    if (!startedAt) return "--";
    const start = new Date(startedAt).getTime();
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

  function formatCost(j: ReviewJob): string {
    const cost = panelCostUsd(j, members);
    if (cost === null) return "--";
    return `~$${cost.toFixed(2)}`;
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
  class:member
  aria-expanded={expandable ? expanded : undefined}
  role="button"
  tabindex="0"
  onclick={onclick}
  onkeydown={(e) => {
    if (e.key === "Enter" || e.key === " ") onclick();
  }}
>
  <td class="col-id">
    {#if expandable}
      <button
        class="chevron"
        class:open={expanded}
        type="button"
        tabindex="-1"
        aria-label={expanded ? "Collapse panel" : "Expand panel"}
        onclick={(e) => {
          e.stopPropagation();
          ontoggle?.();
        }}
      >
        <ChevronRightIcon size={12} strokeWidth={2} />
      </button>
    {:else if member}
      <span class="member-connector mono" aria-hidden="true">
        {lastMember ? "└" : "├"}
      </span>
    {/if}
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
    {#if member && job.panel_member_name}
      <span class="member-name">{job.panel_member_name}</span>
    {/if}
    {#if panelStatus}
      <span class="panel-status">{panelStatus}</span>
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
    {formatCost(job)}
  </td>
  <td class="col-type">{job.job_type}</td>
  <td class="col-queued" title={job.enqueued_at}>
    {formatRelativeTime(job.enqueued_at)}
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

  .job-row.member td {
    background: var(--bg-inset);
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
    white-space: nowrap;
  }

  .job-row.member .col-id {
    padding-left: 24px;
  }

  .chevron {
    display: inline-flex;
    align-items: center;
    color: var(--text-muted);
    transition: transform 0.1s ease;
    vertical-align: middle;
    margin-right: 2px;
    padding: 0;
    border: 0;
    background: transparent;
    cursor: pointer;
  }

  .chevron.open {
    transform: rotate(90deg);
    color: var(--accent-blue);
  }

  .member-connector {
    color: var(--text-muted);
    margin-right: 2px;
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

  .member-name {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--accent-blue);
    font-weight: 500;
  }

  .panel-status {
    display: block;
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
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
