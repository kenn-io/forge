<script lang="ts">
  import { getStores } from "../../context.js";
  import JobRow from "./JobRow.svelte";

  const stores = getStores();
  const jobsStore = stores.roborevJobs;

  type SortColumn =
    | "id"
    | "status"
    | "verdict"
    | "agent"
    | "elapsed"
    | "cost"
    | "job_type"
    | "enqueued_at";

  interface ColumnDef {
    key: SortColumn;
    label: string;
    sortable: boolean;
  }

  const columns: ColumnDef[] = [
    { key: "id", label: "ID", sortable: true },
    {
      key: "id",
      label: "Repo / Branch / Ref",
      sortable: false,
    },
    { key: "agent", label: "Agent", sortable: true },
    { key: "status", label: "Status", sortable: true },
    { key: "verdict", label: "Verdict", sortable: true },
    {
      key: "elapsed",
      label: "Elapsed",
      sortable: true,
    },
    {
      key: "cost",
      label: "Cost",
      sortable: true,
    },
    {
      key: "job_type",
      label: "Type",
      sortable: true,
    },
    {
      key: "enqueued_at",
      label: "Queued",
      sortable: true,
    },
  ];

  function sortIndicator(col: ColumnDef): string {
    if (!col.sortable) return "";
    if (jobsStore?.getSortColumn() !== col.key) return "";
    return jobsStore?.getSortDirection() === "asc"
      ? " \u2191"
      : " \u2193";
  }

  function handleHeaderClick(col: ColumnDef): void {
    if (!col.sortable) return;
    jobsStore?.setSortColumn(col.key);
  }
</script>

<div class="table-wrapper">
  <table class="job-table">
    <thead>
      <tr>
        {#each columns as col (col.label)}
          <th
            class:sortable={col.sortable}
            onclick={() => handleHeaderClick(col)}
          >
            {col.label}{sortIndicator(col)}
          </th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#if jobsStore}
        {#each jobsStore.getJobs() as job (job.id)}
          <JobRow
            {job}
            selected={jobsStore.getSelectedJobId() === job.id}
            highlighted={jobsStore.getHighlightedJobId() === job.id}
            onclick={() => jobsStore.selectJob(job.id)}
          />
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
    <div class="empty-state">No jobs found</div>
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
  .table-wrapper {
    overflow-y: auto;
    flex: 1;
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

  th {
    padding: 6px 10px;
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-muted);
    text-align: left;
    background: var(--bg-inset);
    border-bottom: 1px solid var(--border-default);
    white-space: nowrap;
    user-select: none;
  }

  th.sortable {
    cursor: pointer;
  }

  th.sortable:hover {
    color: var(--text-primary);
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

  .empty-state {
    padding: 32px;
    text-align: center;
    font-size: var(--font-size-md);
    color: var(--text-muted);
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
    opacity: 0.5;
    cursor: default;
  }
</style>
