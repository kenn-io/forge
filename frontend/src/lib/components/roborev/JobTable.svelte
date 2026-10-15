<script lang="ts">
  import { EmptyState, TableHeaderCell, type SortDirection } from "@kenn-io/kit-ui";
  import { getStores } from "../../context.js";
  import { isPanelParent } from "../../utils/roborev-panel.js";
  import JobRow from "./JobRow.svelte";

  const stores = getStores();
  const jobsStore = stores.roborevJobs;

  type SortColumn =
    | "id"
    | "repo"
    | "status"
    | "verdict"
    | "agent"
    | "elapsed"
    | "cost"
    | "job_type"
    | "enqueued_at";

  const columns: { key: SortColumn; label: string }[] = [
    { key: "id", label: "ID" },
    { key: "repo", label: "Repo / Branch / Ref" },
    { key: "agent", label: "Agent" },
    { key: "status", label: "Status" },
    { key: "verdict", label: "Verdict" },
    { key: "elapsed", label: "Elapsed" },
    { key: "cost", label: "Cost" },
    { key: "job_type", label: "Type" },
    { key: "enqueued_at", label: "Queued" },
  ];

  function directionFor(key: SortColumn): SortDirection | null {
    if (jobsStore?.getSortColumn() !== key) return null;
    return jobsStore.getSortDirection();
  }
</script>

<!-- Scrollable regions need keyboard access. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div class="table-wrapper" role="region" aria-label="Review jobs" tabindex="0">
  <table class="job-table">
    <thead>
      <tr>
        {#each columns as col (col.key)}
          <TableHeaderCell
            label={col.label}
            sortable
            sortDirection={directionFor(col.key)}
            onsort={() => jobsStore?.setSortColumn(col.key)}
          />
        {/each}
      </tr>
    </thead>
    <tbody>
      {#if jobsStore}
        {#each jobsStore.getJobs() as job (job.id)}
          {@const runUuid = job.panel_run_uuid ?? undefined}
          {@const expandable = isPanelParent(job) && runUuid !== undefined}
          {@const expanded =
            expandable &&
            runUuid !== undefined &&
            jobsStore.isPanelExpanded(runUuid)}
          {@const members =
            runUuid !== undefined
              ? jobsStore.getPanelMembers(runUuid)
              : undefined}
          {@const memberError =
            runUuid !== undefined
              ? jobsStore.getPanelMemberError(runUuid)
              : undefined}
          <JobRow
            {job}
            {members}
            {expandable}
            {expanded}
            selected={jobsStore.getSelectedJobId() === job.id}
            highlighted={jobsStore.getHighlightedJobId() === job.id}
            onclick={() => jobsStore.selectJob(job.id)}
            ontoggle={() => jobsStore.togglePanel(job)}
          />
          {#if expanded && runUuid !== undefined}
            {#if jobsStore.isLoadingMembers(runUuid) && members === undefined}
              <tr class="members-status-row">
                <td colspan={columns.length}>Loading reviewers…</td>
              </tr>
            {:else}
              {#each members ?? [] as panelMember, i (panelMember.id)}
                <JobRow
                  job={panelMember}
                  member
                  selected={jobsStore.getSelectedJobId() === panelMember.id}
                  highlighted={jobsStore.getHighlightedJobId() === panelMember.id}
                  onclick={() => jobsStore.selectJob(panelMember.id)}
                />
              {/each}
              {#if jobsStore.isLoadingMembers(runUuid)}
                <tr class="members-status-row">
                  <td colspan={columns.length}>Refreshing reviewers…</td>
                </tr>
              {/if}
              {#if memberError}
                <tr class="members-status-row error">
                  <td colspan={columns.length}>
                    Could not refresh reviewers.
                    <button
                      type="button"
                      class="members-retry"
                      onclick={() => jobsStore.refreshPanelMembers(runUuid)}
                    >
                      Retry
                    </button>
                  </td>
                </tr>
              {/if}
            {/if}
          {/if}
        {/each}
      {/if}
    </tbody>
  </table>

  {#if jobsStore?.isLoading()}
    <div class="loading-bar">Loading...</div>
  {/if}

  {#if jobsStore?.getError()}
    <div class="error-bar">
      {jobsStore.getError()}
    </div>
  {/if}

  {#if jobsStore && !jobsStore.isLoading() && jobsStore.getJobs().length === 0}
    <EmptyState title="No jobs found" />
  {/if}

  {#if jobsStore?.getHasMore()}
    <div class="load-more">
      <button
        class="load-more-btn"
        disabled={jobsStore.isLoading()}
        onclick={() => jobsStore.loadMore()}
      >
        Load more
      </button>
    </div>
  {/if}
</div>

<style>
  /* Tables scroll both axes in narrow hosts (640px workspace sidebar), so
     this stays a native scroller instead of the vertical-only ScrollBox:
     hiding the native bars would drop the horizontal affordance, and a
     nested x-scroller would detach the sticky thead from the scrollport. */
  .table-wrapper {
    overflow: auto;
    flex: 1;
    min-height: 0;
  }

  .job-table {
    width: 100%;
    border-collapse: collapse;
    table-layout: auto;
  }

  thead {
    position: sticky;
    top: 0;
    z-index: 1;
  }

  .job-table :global(tbody tr:nth-child(even)) {
    background: var(--bg-inset);
  }

  .job-table :global(tbody tr:nth-child(even):hover) {
    background: var(--bg-surface-hover);
  }

  .loading-bar {
    padding: 12px;
    text-align: center;
    font-size: var(--font-size-sm);
    color: var(--text-muted);
  }

  .error-bar {
    padding: 12px;
    text-align: center;
    font-size: var(--font-size-sm);
    color: var(--accent-red);
  }

  .members-status-row td {
    padding: 6px 10px 6px 34px;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    background: var(--bg-inset);
  }

  .members-status-row.error td {
    color: var(--accent-red);
  }

  .members-retry {
    margin-left: 8px;
    border: 0;
    padding: 0;
    background: transparent;
    color: var(--text-primary);
    font: inherit;
    text-decoration: underline;
    cursor: pointer;
  }

  .load-more {
    padding: 8px 12px;
    text-align: center;
    border-top: 1px solid var(--border-muted);
  }

  .load-more-btn {
    padding: 4px 16px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .load-more-btn:hover {
    background: var(--bg-surface-hover);
  }

  .load-more-btn:disabled {
    opacity: var(--opacity-disabled);
    cursor: default;
  }
</style>
