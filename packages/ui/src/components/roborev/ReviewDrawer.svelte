<script lang="ts">
  import { copyToClipboard } from "@kenn-io/kit-ui";
  import { getStores } from "../../context.js";
  import StatusBadge from "./StatusBadge.svelte";
  import VerdictBadge from "./VerdictBadge.svelte";
  import ReviewContent from "./ReviewContent.svelte";
  import ResponseList from "./ResponseList.svelte";
  import LogViewer from "./LogViewer.svelte";
  import PromptViewer from "./PromptViewer.svelte";

  // NOTE: intentionally NOT kit-ui DetailDrawer. This is a resizable bottom
  // dock (height: 50vh; resize: vertical; accent border-top) that lives
  // inline in the reviews layout, not a floating dimmed right side-sheet.
  // The kit-ui-check-ignore markers below record that deliberate choice.

  interface Props {
    activeTab?: "review" | "log" | "prompt";
  }
  let { activeTab = $bindable("review") }: Props =
    $props();

  const stores = getStores();

  // Prefer the live table row (updated by SSE/mutations).
  // Fall back to the review store's fetched job for
  // off-page deep links where the job isn't in the table.
  const selectedJob = $derived(
    stores.roborevJobs?.getJobs().find(
      (j) =>
        j.id ===
        stores.roborevJobs?.getSelectedJobId(),
    ) ?? stores.roborevReview?.getSelectedJob(),
  );

  const isOpen = $derived(
    stores.roborevJobs?.getSelectedJobId() !== undefined,
  );

  function close(): void {
    stores.roborevJobs?.deselectJob();
  }

  function shortRef(ref: string): string {
    if (ref.length > 10) return ref.slice(0, 8);
    return ref;
  }

  async function copyOutput(): Promise<void> {
    const output =
      stores.roborevReview?.getOutput() ?? "";
    await copyToClipboard(output);
  }

  function handleCloseReview(): void {
    const jobId =
      stores.roborevJobs?.getSelectedJobId();
    if (jobId !== undefined) {
      void stores.roborevReview?.closeReview(jobId);
    }
  }

  function handleRerun(): void {
    const jobId =
      stores.roborevJobs?.getSelectedJobId();
    if (jobId !== undefined) {
      void stores.roborevJobs?.rerunJob(jobId);
    }
  }

  function handleCancel(): void {
    const jobId =
      stores.roborevJobs?.getSelectedJobId();
    if (jobId !== undefined) {
      void stores.roborevJobs?.cancelJob(jobId);
    }
  }

  const canCancel = $derived(
    selectedJob?.status === "queued" ||
      selectedJob?.status === "running",
  );

  const hasReview = $derived(
    stores.roborevReview?.getReview() != null,
  );

  const reviewIsClosed = $derived(
    stores.roborevReview?.isClosed() ?? false,
  );
</script>

{#if isOpen}
  <!-- kit-ui-check-ignore: resizable inline bottom dock, not a kit side-sheet -->
  <div class="drawer">
    <!-- kit-ui-check-ignore: resizable inline bottom dock, not a kit side-sheet -->
    <div class="drawer-header">
      <div class="header-start">
        {#if selectedJob}
          <span class="job-id">
            #{selectedJob.id}
          </span>
          <VerdictBadge
            verdict={selectedJob.verdict}
          />
          <span class="header-meta">
            {#if selectedJob.repo_name}
              <span class="repo-name">
                {selectedJob.repo_name}
              </span>
            {/if}
            {#if selectedJob.branch}
              <span class="branch">
                {selectedJob.branch}
              </span>
            {/if}
            <span
              class="git-ref"
              title={selectedJob.git_ref}
            >
              {shortRef(selectedJob.git_ref)}
            </span>
          </span>
          <span class="header-agent">
            {selectedJob.agent}
            {#if selectedJob.model}
              / {selectedJob.model}
            {/if}
          </span>
          {#if selectedJob.review_type}
            <span class="review-type">
              {selectedJob.review_type}
            </span>
          {/if}
          <StatusBadge status={selectedJob.status} />
        {/if}
      </div>
      <button
        class="close-btn"
        onclick={close}
        title="Close drawer"
      >
        <svg
          width="16"
          height="16"
          viewBox="0 0 16 16"
          fill="currentColor"
        >
          <path
            d="M3.72 3.72a.75.75 0 011.06 0L8
              6.94l3.22-3.22a.75.75 0 111.06
              1.06L9.06 8l3.22 3.22a.75.75 0
              11-1.06 1.06L8 9.06l-3.22
              3.22a.75.75 0 01-1.06-1.06L6.94
              8 3.72 4.78a.75.75 0 010-1.06z"
          />
        </svg>
      </button>
    </div>

    <div class="tab-bar">
      <button
        class="tab"
        class:active={activeTab === "review"}
        onclick={() => (activeTab = "review")}
      >
        Review
      </button>
      <button
        class="tab"
        class:active={activeTab === "log"}
        onclick={() => (activeTab = "log")}
      >
        Log
      </button>
      <button
        class="tab"
        class:active={activeTab === "prompt"}
        onclick={() => (activeTab = "prompt")}
      >
        Prompt
      </button>
    </div>

    <!-- kit-ui-check-ignore: resizable inline bottom dock, not a kit side-sheet -->
    <div class="drawer-body">
      {#if activeTab === "review"}
        <ReviewContent />
        <div class="responses-section">
          <ResponseList />
        </div>
      {:else if activeTab === "log"}
        {#if selectedJob}
          <LogViewer
            jobId={selectedJob.id}
            jobStatus={selectedJob.status}
          />
        {/if}
      {:else if activeTab === "prompt"}
        <PromptViewer />
      {/if}
    </div>

    <!-- kit-ui-check-ignore: resizable inline bottom dock, not a kit side-sheet -->
    <div class="drawer-footer">
      <div class="footer-actions">
        {#if hasReview}
          <button
            class="action-btn"
            onclick={handleCloseReview}
            title={reviewIsClosed
              ? "Reopen review"
              : "Close review"}
          >
            {reviewIsClosed ? "Reopen" : "Close Review"}
          </button>
        {/if}
        <button
          class="action-btn"
          onclick={handleRerun}
          title="Rerun this job"
        >
          Rerun
        </button>
        {#if canCancel}
          <button
            class="action-btn action-btn-danger"
            onclick={handleCancel}
            title="Cancel this job"
          >
            Cancel
          </button>
        {/if}
        <button
          class="action-btn"
          onclick={() => void copyOutput()}
          title="Copy review output"
        >
          Copy Output
        </button>
      </div>
      {#if selectedJob?.token_usage}
        <span class="token-usage">
          {selectedJob.token_usage}
        </span>
      {/if}
    </div>
  </div>
{/if}

<style>
  /* kit-ui-check-ignore: resizable inline bottom dock, not a kit side-sheet */
  .drawer {
    display: flex;
    flex-direction: column;
    height: 50vh;
    min-height: 200px;
    max-height: 80vh;
    border-top: 2px solid var(--accent-blue);
    background: var(--bg-surface);
    resize: vertical;
    overflow: hidden;
  }

  /* kit-ui-check-ignore: resizable inline bottom dock, not a kit side-sheet */
  .drawer-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
    min-height: 36px;
  }

  .header-start {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    overflow: hidden;
    min-width: 0;
  }

  .job-id {
    font-size: var(--font-size-md);
    font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap;
  }

  .header-meta {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: var(--font-size-sm);
  }

  .repo-name {
    font-weight: 500;
    color: var(--text-primary);
  }

  .branch {
    color: var(--accent-blue);
  }

  .git-ref {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .header-agent {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .review-type {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    padding: 1px 6px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    white-space: nowrap;
  }

  .close-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    flex-shrink: 0;
  }

  .close-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .tab-bar {
    display: flex;
    gap: 0;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
    padding: 0 12px;
  }

  .tab {
    padding: 6px 14px;
    border: none;
    border-bottom: 2px solid transparent;
    background: transparent;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 500;
    cursor: pointer;
    margin-bottom: -1px;
  }

  .tab:hover {
    color: var(--text-primary);
  }

  .tab.active {
    color: var(--accent-blue);
    border-bottom-color: var(--accent-blue);
  }

  /* kit-ui-check-ignore: resizable inline bottom dock, not a kit side-sheet */
  .drawer-body {
    flex: 1;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }

  .responses-section {
    padding: 12px 20px 16px;
    border-top: 1px solid var(--border-muted);
  }

  /* kit-ui-check-ignore: resizable inline bottom dock, not a kit side-sheet */
  .drawer-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 12px;
    border-top: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .footer-actions {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }

  .action-btn {
    padding: 4px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    cursor: pointer;
    white-space: nowrap;
  }

  .action-btn:hover {
    background: var(--bg-surface-hover);
  }

  .action-btn-danger {
    color: var(--review-failed);
    border-color: var(--review-failed);
  }

  .action-btn-danger:hover {
    background: color-mix(
      in srgb,
      var(--review-failed) 8%,
      var(--bg-surface)
    );
  }

  .token-usage {
    font-size: var(--font-size-xs);
    font-family: var(--font-mono);
    color: var(--text-muted);
    white-space: nowrap;
  }
</style>
