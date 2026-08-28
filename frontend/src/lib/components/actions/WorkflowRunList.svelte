<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import { isSafeExternalHTTPURL } from "../../utils/safe-external-url.js";
  import type { components } from "../../api/generated/schema.js";

  type Run = components["schemas"]["WorkflowRunResponse"];
  type Job = components["schemas"]["WorkflowRunJobResponse"];

  interface Props {
    runs: readonly Run[];
    jobs: Readonly<Record<string, readonly Job[]>>;
    loadingJobs: readonly string[];
    onexpand: (runId: string) => void;
    oncollapse: (runId: string) => void;
  }

  let { runs, jobs, loadingJobs, onexpand, oncollapse }: Props = $props();
  let expandedRuns = $state<Record<string, boolean>>({});
  let expandedJobs = $state<Record<string, boolean>>({});

  function statusText(status: string, conclusion: string): string {
    return conclusion ? `${status} · ${conclusion}` : status;
  }
  function toggleRun(id: string): void {
    const expanded = expandedRuns[id] === true;
    expandedRuns[id] = !expanded;
    if (expanded) oncollapse(id); else onexpand(id);
  }
  function providerName(url: string): string {
    try {
      const host = new URL(url).hostname;
      if (host === "github.com" || host.endsWith(".github.com")) return "GitHub";
      if (host === "gitlab.com" || host.endsWith(".gitlab.com")) return "GitLab";
      return "provider";
    } catch { return "provider"; }
  }
</script>

<div class="run-list" role="list" aria-label="Workflow runs">
  {#each runs as run (run.id)}
    {@const safeRunURL = run.web_url && isSafeExternalHTTPURL(run.web_url) ? run.web_url : undefined}
    <section class="run" role="listitem">
      <div class="run-row">
        <Button class="run-disclosure" surface="ghost" ariaExpanded={expandedRuns[run.id] === true} ariaLabel={`Run ${run.run_number} ${run.name}`} onclick={() => toggleRun(run.id)}>
          <ChevronRightIcon size={14} aria-hidden="true" class={expandedRuns[run.id] === true ? "expanded" : undefined} />
          <span class="number">#{run.run_number}</span><strong>{run.name}</strong>
          <code>{run.ref}</code><span>{run.actor}</span>
          <span class="status" data-status={run.conclusion || run.status}>{statusText(run.status, run.conclusion)}</span>
          {#if run.created_at}<time datetime={run.created_at}>{new Date(run.created_at).toLocaleString()}</time>{/if}
          <code title={run.head_sha}>{run.head_sha.slice(0, 7)}</code>
        </Button>
        {#if safeRunURL}
          <a href={safeRunURL} target="_blank" rel="noopener" aria-label={`Open on ${providerName(safeRunURL)}`}><ExternalLinkIcon size={14} aria-hidden="true" /></a>
        {/if}
      </div>
      {#if expandedRuns[run.id]}
        <div class="jobs" aria-label={`Jobs for run ${run.run_number}`}>
          {#if loadingJobs.includes(run.id)}
            <p role="status">Loading jobs…</p>
          {:else if (jobs[run.id]?.length ?? 0) === 0}
            <p>No jobs available.</p>
          {:else}
            {#each jobs[run.id] ?? [] as job (job.id)}
              <div class="job">
                <button class="job-row" type="button" aria-expanded={expandedJobs[job.id] === true} onclick={() => { expandedJobs[job.id] = expandedJobs[job.id] !== true; }}>
                  <ChevronRightIcon size={13} aria-hidden="true" class={expandedJobs[job.id] === true ? "expanded" : undefined} />
                  <span>{job.name}</span><span class="status" data-status={job.conclusion || job.status}>{statusText(job.status, job.conclusion)}</span>
                </button>
                {#if expandedJobs[job.id] && job.steps}
                  <ol class="steps" aria-label={`${job.name} steps`}>
                    {#each job.steps as step (step.number)}<li><span>{step.name}</span><span class="status" data-status={step.conclusion || step.status}>{statusText(step.status, step.conclusion)}</span></li>{/each}
                  </ol>
                {/if}
              </div>
            {/each}
          {/if}
        </div>
      {/if}
    </section>
  {/each}
</div>

<style>
  .run-list { display: grid; border-block-start: 1px solid var(--border-subtle); }
  .run { border-block-end: 1px solid var(--border-subtle); }
  .run-row { display: grid; grid-template-columns: minmax(0, 1fr) 30px; align-items: center; }
  :global(.run-disclosure) { min-width: 0; justify-content: start; display: grid; grid-template-columns: auto auto minmax(100px, 1fr) auto auto auto auto auto; gap: var(--space-3); text-align: left; }
  :global(.run-disclosure svg), .job-row :global(svg) { transition: none; }
  :global(.run-disclosure svg.expanded), .job-row :global(svg.expanded) { transform: rotate(90deg); }
  a { display: grid; place-items: center; color: var(--text-secondary); min-height: 30px; }
  code, time, .status, .jobs { font-size: var(--font-size-xs); color: var(--text-secondary); }
  .jobs { padding: 0 0 var(--space-2) var(--space-6); }
  .jobs p { margin: var(--space-2); }
  .job-row, .steps li { width: 100%; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; gap: var(--space-2); align-items: center; min-height: 28px; border: 0; background: transparent; color: var(--text-primary); text-align: left; }
  .job-row { cursor: pointer; }
  .steps { margin: 0; padding-inline-start: var(--space-6); }
  .steps li { grid-template-columns: minmax(0, 1fr) auto; }
  [data-status="success"] { color: var(--status-success-text, var(--text-success)); }
  [data-status="failure"], [data-status="cancelled"] { color: var(--status-danger-text, var(--text-danger)); }
</style>
