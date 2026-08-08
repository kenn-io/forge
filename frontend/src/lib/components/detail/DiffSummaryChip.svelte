<script lang="ts">
  import {
    DiffSummaryFilesResult,
    summarizeDiffFiles,
    type DiffLineSummary,
  } from "./diff-summary.js";
  import { DiffStats, Tooltip } from "@kenn-io/kit-ui";

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

  /* kit Tooltip sets aria-describedby on its non-focusable wrapper span, not
     on the real trigger. Reference the tooltip content from the button
     directly: while closed the id resolves to nothing (ignored by AT), while
     open it resolves to the summary. */
  const summaryContentId = $props.id();

  /* Whether the pointer/focus is on the trigger — drives lazy loading and
     the refetch-on-rekey behavior while the tooltip is held open. */
  let active = false;
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
        if (active) void ensureSummary();
      }
    }
  }

  function handleEnter(): void {
    active = true;
    void ensureSummary();
  }

  function handleLeave(): void {
    active = false;
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
    if (active) void ensureSummary();
  });
</script>

<Tooltip class="diff-summary-popover" align="start" openDelayMs={0}>
  {#snippet content()}
    <!-- The summary loads after the tooltip is already described to
         assistive tech; a live region announces the async transition
         from Loading to the totals (or an error). -->
    <div id={summaryContentId} aria-live="polite">
      {#if error}
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
      {:else}
        <div class="diff-summary-state">Loading...</div>
      {/if}
    </div>
  {/snippet}
  <button
    type="button"
    class="diff-summary-trigger"
    aria-describedby={summaryContentId}
    onmouseenter={handleEnter}
    onmouseleave={handleLeave}
    onfocusin={handleEnter}
    onfocusout={handleLeave}
  >
    <DiffStats {additions} {deletions} />
  </button>
</Tooltip>

<style>
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
    gap: var(--space-2);
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
