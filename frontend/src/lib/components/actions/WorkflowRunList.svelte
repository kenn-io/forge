<script lang="ts">
  import {
    Button,
    Chip,
    Table,
    TableHeaderCell,
    formatRelativeTime,
    type SortDirection,
  } from "@kenn-io/kit-ui";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import { isSafeExternalHTTPURL } from "../../utils/safe-external-url.js";
  import type { WorkflowRunJobResponse } from "../../api/generated/models/workflowRunJobResponse.js";
  import type { WorkflowRunResponse } from "../../api/generated/models/workflowRunResponse.js";
  import type { WorkflowActionsError } from "../../stores/workflow-actions.svelte.js";
  import { workflowActionsErrorMessage } from "./workflow-dispatch-presentation.js";
  import { workflowStatusPresentation } from "./workflow-run-status.js";

  type Run = WorkflowRunResponse;
  type Job = WorkflowRunJobResponse;
  type SortKey = "run" | "branch" | "actor" | "status" | "started";

  interface Props {
    runs: readonly Run[];
    jobs: Readonly<Record<string, readonly Job[]>>;
    jobErrors: Readonly<Record<string, WorkflowActionsError | undefined>>;
    loadingJobs: readonly string[];
    onexpand: (runId: string) => void;
  }

  const COLUMN_COUNT = 7;

  let { runs, jobs, jobErrors, loadingJobs, onexpand }: Props = $props();
  let expandedRuns = $state<Record<string, boolean>>({});
  let expandedJobs = $state<Record<string, boolean>>({});
  // null keeps the provider's order (newest first).
  let sort = $state<{ key: SortKey; direction: SortDirection } | null>(null);

  const sortedRuns = $derived.by(() => {
    if (!sort) return runs;
    const { key, direction } = sort;
    const sign = direction === "asc" ? 1 : -1;
    return [...runs].sort((a, b) => sign * compareRuns(key, a, b) || b.run_number - a.run_number);
  });

  function compareRuns(key: SortKey, a: Run, b: Run): number {
    switch (key) {
      case "run":
        return a.run_number - b.run_number;
      case "branch":
        return a.ref.localeCompare(b.ref);
      case "actor":
        return a.actor.localeCompare(b.actor);
      case "status":
        return workflowStatusPresentation(a.status, a.conclusion).rank
          - workflowStatusPresentation(b.status, b.conclusion).rank;
      case "started":
        return (a.created_at ?? "").localeCompare(b.created_at ?? "");
    }
  }

  function sortBy(key: SortKey): void {
    const initial: SortDirection = key === "run" || key === "started" ? "desc" : "asc";
    if (sort?.key !== key) {
      sort = { key, direction: initial };
    } else if (sort.direction === initial) {
      sort = { key, direction: initial === "asc" ? "desc" : "asc" };
    } else {
      sort = null;
    }
  }

  function directionFor(key: SortKey): SortDirection | null {
    return sort?.key === key ? sort.direction : null;
  }

  function rawStatus(status: string, conclusion: string): string {
    return conclusion ? `${status} · ${conclusion}` : status;
  }
  function toggleRun(id: string): void {
    const expanded = expandedRuns[id] === true;
    expandedRuns[id] = !expanded;
    if (!expanded) onexpand(id);
  }
  function providerName(url: string): string {
    try {
      const parsed = new URL(url);
      const hostname = parsed.hostname;
      if (hostname === "github.com" || hostname.endsWith(".github.com")) return "GitHub";
      if (hostname === "gitlab.com" || hostname.endsWith(".gitlab.com")) return "GitLab";
      return parsed.host || "provider";
    } catch { return "provider"; }
  }
</script>

{#snippet statusChip(status: string, conclusion: string)}
  {@const presentation = workflowStatusPresentation(status, conclusion)}
  <span class="status" title={rawStatus(status, conclusion)}>
    <Chip size="xs" tone={presentation.tone} dot uppercase={false}>{presentation.label}</Chip>
  </span>
{/snippet}

<div class="workflow-runs">
  <Table ariaLabel="Workflow runs" zebra={false} class="workflow-runs__table">
    {#snippet header()}
      <TableHeaderCell label="Run" sortable sortDirection={directionFor("run")} onsort={() => sortBy("run")} />
      <TableHeaderCell label="Branch" class="runs-col-branch" sortable sortDirection={directionFor("branch")} onsort={() => sortBy("branch")} />
      <TableHeaderCell label="Actor" class="runs-col-actor" sortable sortDirection={directionFor("actor")} onsort={() => sortBy("actor")} />
      <TableHeaderCell label="Status" class="runs-col-status" sortable sortDirection={directionFor("status")} onsort={() => sortBy("status")} />
      <TableHeaderCell label="Started" class="runs-col-started" sortable sortDirection={directionFor("started")} onsort={() => sortBy("started")} />
      <TableHeaderCell label="Commit" class="runs-col-commit" />
      <TableHeaderCell class="runs-col-link"><span class="kit-sr-only">Provider link</span></TableHeaderCell>
    {/snippet}
    {#each sortedRuns as run (run.id)}
      {@const expanded = expandedRuns[run.id] === true}
      {@const safeRunURL = run.web_url && isSafeExternalHTTPURL(run.web_url) ? run.web_url : undefined}
      <tr class="run-row" class:run-row--expanded={expanded}>
        <td class="run-cell">
          <button
            type="button"
            class="run-disclosure"
            aria-expanded={expanded}
            aria-label={`Run ${run.run_number} ${run.name}`}
            title={`#${run.run_number} ${run.name}`}
            onclick={() => toggleRun(run.id)}
          >
            <ChevronRightIcon size={13} aria-hidden="true" class={expanded ? "expanded" : undefined} />
            <span class="run-number">#{run.run_number}</span>
            <span class="run-name">{run.name}</span>
          </button>
        </td>
        <td class="runs-col-branch"><code class="clip" title={run.ref}>{run.ref}</code></td>
        <td class="runs-col-actor"><span class="clip" title={run.actor}>{run.actor}</span></td>
        <td class="runs-col-status">{@render statusChip(run.status, run.conclusion)}</td>
        <td class="runs-col-started">
          {#if run.created_at}
            <time datetime={run.created_at} title={new Date(run.created_at).toLocaleString()}>{formatRelativeTime(run.created_at)}</time>
          {/if}
        </td>
        <td class="runs-col-commit"><code title={run.head_sha}>{run.head_sha.slice(0, 7)}</code></td>
        <td class="runs-col-link">
          {#if safeRunURL}
            <a href={safeRunURL} target="_blank" rel="noopener" aria-label={`Open on ${providerName(safeRunURL)}`}><ExternalLinkIcon size={13} aria-hidden="true" /></a>
          {/if}
        </td>
      </tr>
      {#if expanded}
        {@const jobError = jobErrors[run.id]}
        <tr class="run-detail">
          <td colspan={COLUMN_COUNT}>
            <!-- Narrow panes drop columns; the detail keeps every field reachable. -->
            <dl class="run-meta">
              <div><dt>Event</dt><dd>{run.event}</dd></div>
              <div><dt>Branch</dt><dd><code>{run.ref}</code></dd></div>
              <div><dt>Actor</dt><dd>{run.actor}</dd></div>
              <div><dt>Commit</dt><dd><code>{run.head_sha.slice(0, 7)}</code></dd></div>
              {#if run.created_at}<div><dt>Started</dt><dd>{new Date(run.created_at).toLocaleString()}</dd></div>{/if}
            </dl>
            <div class="jobs" aria-label={`Jobs for run ${run.run_number}`}>
              {#if loadingJobs.includes(run.id)}
                <p role="status">Loading jobs…</p>
              {:else if jobError}
                <p class="jobs-error" role="alert">{workflowActionsErrorMessage(jobError, "Workflow jobs could not be loaded.")}</p>
                <Button size="sm" surface="soft" onclick={() => onexpand(run.id)}>Retry jobs</Button>
              {:else if (jobs[run.id]?.length ?? 0) === 0}
                <p>No jobs available.</p>
              {:else}
                {#each jobs[run.id] ?? [] as job (job.id)}
                  {@const jobExpanded = expandedJobs[job.id] === true}
                  <div class="job">
                    <button class="job-row" type="button" aria-expanded={jobExpanded} onclick={() => { expandedJobs[job.id] = !jobExpanded; }}>
                      <ChevronRightIcon size={12} aria-hidden="true" class={jobExpanded ? "expanded" : undefined} />
                      <span class="job-name">{job.name}</span>
                      {@render statusChip(job.status, job.conclusion)}
                    </button>
                    {#if jobExpanded && job.steps}
                      <ol class="steps" aria-label={`${job.name} steps`}>
                        {#each job.steps as step (step.number)}
                          <li><span class="step-name">{step.name}</span>{@render statusChip(step.status, step.conclusion)}</li>
                        {/each}
                      </ol>
                    {/if}
                  </div>
                {/each}
              {/if}
            </div>
          </td>
        </tr>
      {/if}
    {/each}
  </Table>
</div>

<style>
  .workflow-runs { container-type: inline-size; min-width: 0; }

  /* The pane's ScrollBox owns scrolling, so the sticky header must resolve
   * against it rather than the table's own wrapper. */
  .workflow-runs :global(.workflow-runs__table) { overflow: visible; }
  .workflow-runs :global(.kit-table) { table-layout: fixed; }

  .workflow-runs :global(.runs-col-branch) { width: 20%; }
  .workflow-runs :global(.runs-col-actor) { width: 16%; }
  .workflow-runs :global(.runs-col-status) { width: 116px; }
  .workflow-runs :global(.runs-col-started) { width: 84px; }
  .workflow-runs :global(.runs-col-commit) { width: 76px; }
  .workflow-runs :global(.runs-col-link) { width: 36px; padding-inline: 0; }

  td { overflow: hidden; white-space: nowrap; vertical-align: middle; }
  td.run-cell { padding-block: 0; padding-inline-start: var(--space-2); }
  td.runs-col-link { text-align: center; }
  .run-row--expanded > td { border-block-end-color: transparent; background: var(--bg-inset); }

  .run-disclosure {
    width: 100%;
    min-width: 0;
    min-height: 30px;
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    text-align: start;
    cursor: pointer;
  }
  .run-disclosure :global(svg), .job-row :global(svg) { flex: 0 0 auto; color: var(--text-muted); }
  .run-disclosure :global(svg.expanded), .job-row :global(svg.expanded) { transform: rotate(90deg); }
  .run-number { flex: 0 0 auto; color: var(--text-secondary); font-variant-numeric: tabular-nums; }
  .run-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; font-weight: 550; }

  .clip { display: inline-block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; }
  code { font-family: var(--font-mono); font-size: var(--font-size-xs); }
  td code, td .clip, td time { vertical-align: middle; }
  time { font-size: var(--font-size-xs); font-variant-numeric: tabular-nums; }
  .status { display: inline-flex; }
  a { display: inline-grid; place-items: center; width: 24px; height: 24px; border-radius: var(--radius-sm); color: var(--text-muted); }
  a:hover { color: var(--text-primary); background: var(--bg-surface-hover); }

  .run-detail > td {
    padding: var(--space-2) var(--space-4) var(--space-4) var(--space-7);
    background: var(--bg-inset);
    white-space: normal;
  }
  .run-meta { display: flex; flex-wrap: wrap; gap: var(--space-1) var(--space-5); margin: 0 0 var(--space-3); font-size: var(--font-size-xs); }
  .run-meta > div { display: flex; gap: var(--space-2); min-width: 0; }
  .run-meta dt { color: var(--text-muted); }
  .run-meta dd { margin: 0; min-width: 0; overflow-wrap: anywhere; color: var(--text-secondary); }

  .jobs { min-width: 0; max-width: 760px; font-size: var(--font-size-sm); color: var(--text-secondary); }
  .jobs p { margin: 0 0 var(--space-2); }
  .jobs-error { color: var(--status-danger-text, var(--text-danger)); }
  .job + .job { border-block-start: 1px solid var(--border-muted); }
  .job-row, .steps li {
    box-sizing: border-box;
    width: 100%;
    min-width: 0;
    min-height: 28px;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--space-2);
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    text-align: start;
  }
  .job-row { cursor: pointer; }
  .job-row:hover .job-name { text-decoration: underline; }
  .job-name, .step-name { min-width: 0; overflow-wrap: anywhere; }
  .steps { margin: 0 0 var(--space-2); padding-inline-start: var(--space-7); list-style: none; }
  .steps li { grid-template-columns: minmax(0, 1fr) auto; padding-block: var(--space-1); color: var(--text-secondary); }

  /* Collapse rather than remove narrow-pane columns: the detail row spans a
   * fixed column count, and removed cells would leave phantom columns that
   * steal width under the fixed table layout. */
  @container (max-width: 720px) {
    .workflow-runs :global(.runs-col-commit) { width: 0; padding: 0; overflow: hidden; visibility: hidden; }
  }
  @container (max-width: 600px) {
    .workflow-runs :global(.runs-col-actor) { width: 0; padding: 0; overflow: hidden; visibility: hidden; }
  }
  @container (max-width: 480px) {
    .workflow-runs :global(.runs-col-branch) { width: 0; padding: 0; overflow: hidden; visibility: hidden; }
  }
</style>
