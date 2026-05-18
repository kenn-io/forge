<script lang="ts">
  import {
    DiffSummaryFilesResult,
    summarizeDiffFiles,
    type DiffLineSummary,
  } from "./diff-summary.js";
  import DiffStats from "../shared/DiffStats.svelte";

  interface Props {
    additions: number;
    deletions: number;
    summaryKey?: string;
    loadFiles: () => Promise<DiffSummaryFilesResult>;
  }

  const {
    additions,
    deletions,
    summaryKey = "",
    loadFiles,
  }: Props = $props();

  const popoverId = $derived(
    `diff-summary-popover-${
      (summaryKey || "current").replace(/[^a-zA-Z0-9_-]/g, "-")
    }`,
  );
  let open = $state(false);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let summary = $state<DiffLineSummary | null>(null);
  let loadedSummaryKey = $state<string | null>(null);
  let currentSummaryKey = $state<string | null>(null);

  const rows = $derived([
    { key: "plansDocs" as const, label: "Plans/docs" },
    { key: "code" as const, label: "Code" },
    { key: "tests" as const, label: "Tests" },
    { key: "other" as const, label: "Other" },
    { key: "generated" as const, label: "Generated" },
  ]);
  const visibleRows = $derived(
    summary === null
      ? []
      : rows.filter((row) => {
          const totals = summary?.[row.key];
          return (totals?.additions ?? 0) > 0 || (totals?.deletions ?? 0) > 0;
        }),
  );

  async function ensureSummary(): Promise<void> {
    const requestedKey = summaryKey;
    currentSummaryKey ??= requestedKey;
    if (loadedSummaryKey !== requestedKey) {
      summary = null;
      error = null;
      loadedSummaryKey = null;
    }
    if (summary !== null || loading) return;
    loading = true;
    error = null;
    try {
      const result = (await loadFiles()).clone();
      if (requestedKey !== summaryKey) {
        return;
      }
      if (result.stale) {
        summary = null;
        loadedSummaryKey = null;
        error = "Changed files are still refreshing.";
        return;
      }
      summary = summarizeDiffFiles(result.files);
      loadedSummaryKey = requestedKey;
    } catch (err) {
      if (requestedKey === summaryKey) {
        error = err instanceof Error ? err.message : String(err);
      }
    } finally {
      loading = false;
      if (requestedKey !== summaryKey) {
        summary = null;
        error = null;
        loadedSummaryKey = null;
        if (open) void ensureSummary();
      }
    }
  }

  function showPopover(): void {
    open = true;
    void ensureSummary();
  }

  function hidePopover(): void {
    open = false;
  }

  $effect.pre(() => {
    if (currentSummaryKey === null) {
      currentSummaryKey = summaryKey;
      return;
    }
    if (currentSummaryKey === summaryKey) return;
    currentSummaryKey = summaryKey;
    summary = null;
    error = null;
    loadedSummaryKey = null;
    if (open) void ensureSummary();
  });
</script>

<span class="diff-summary">
  <button
    type="button"
    class="chip chip--muted diff-summary-trigger"
    aria-describedby={open ? popoverId : undefined}
    onmouseenter={showPopover}
    onmouseleave={hidePopover}
    onmouseover={showPopover}
    onmouseout={hidePopover}
    onfocus={showPopover}
    onblur={hidePopover}
  >
    <DiffStats {additions} {deletions} />
  </button>

  {#if open}
    <div
      id={popoverId}
      class="diff-summary-popover"
      role="status"
    >
      {#if loading}
        <div class="diff-summary-state">Loading...</div>
      {:else if error}
        <div class="diff-summary-state diff-summary-state--error">
          {error}
        </div>
      {:else if summary}
        <div class="diff-summary-rows">
          {#each visibleRows as row (row.key)}
            <div class="diff-summary-row">
              <span>{row.label}</span>
              <DiffStats
                additions={summary[row.key].additions}
                deletions={summary[row.key].deletions}
              />
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</span>

<style>
  .diff-summary {
    position: relative;
    display: inline-flex;
  }

  .diff-summary-trigger {
    box-sizing: border-box;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 22px;
    padding: 0 8px;
    border: 0;
    border-radius: 10px;
    background: var(--bg-inset);
    color: var(--text-muted);
    font-family: inherit;
    font-size: var(--font-size-xs);
    font-weight: 600;
    line-height: 1;
    white-space: nowrap;
    cursor: default;
    gap: 4px;
  }

  .diff-summary-trigger:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 2px;
  }

  .diff-summary-popover {
    position: absolute;
    z-index: 30;
    top: calc(100% + 8px);
    left: 0;
    width: max-content;
    min-width: 190px;
    max-width: min(260px, calc(100vw - 32px));
    padding: 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md, 8px);
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: 0 12px 30px rgba(0, 0, 0, 0.18);
  }

  .diff-summary-popover::before {
    content: "";
    position: absolute;
    top: -5px;
    left: 18px;
    width: 8px;
    height: 8px;
    transform: rotate(45deg);
    border-left: 1px solid var(--border-default);
    border-top: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .diff-summary-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 16px;
    align-items: center;
    font-size: var(--font-size-sm);
  }

  .diff-summary-rows {
    display: flex;
    flex-direction: column;
    gap: 5px;
  }

  .diff-summary-row {
    color: var(--text-secondary);
  }

  .diff-summary-state {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .diff-summary-state--error {
    color: var(--accent-red);
  }
</style>
