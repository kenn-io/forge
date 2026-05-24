<script module lang="ts">
  const _ciStatusInstanceCounter = { value: 0 };
</script>

<script lang="ts">
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import LoaderCircleIcon from "@lucide/svelte/icons/loader-circle";
  import DotIcon from "@lucide/svelte/icons/dot";
  import XIcon from "@lucide/svelte/icons/x";
  import CheckIcon from "@lucide/svelte/icons/check";
  import MinusIcon from "@lucide/svelte/icons/minus";
  import HelpCircleIcon from "@lucide/svelte/icons/circle-help";
  import type { CICheck } from "../../api/types.js";
  import Chip from "../shared/Chip.svelte";
  import CITokenCluster, { composeAriaLabel } from "../shared/CITokenCluster.svelte";
  import {
    parseCIChecks,
    bucketCIChecks,
    safeDiagnosticText,
    type CIBucket,
  } from "../../utils/ci-buckets.js";
  import {
    warnOnUnknownConclusions,
    warnOnMalformedCIChecksJSON,
  } from "../../utils/ci-buckets-warn.js";
  import { prefersReducedMotion } from "../../utils/prefers-reduced-motion.svelte.js";

  interface Props {
    status: string;
    checksJSON: string;
    detailLoaded: boolean;
    detailSyncing: boolean;
    owner: string;
    name: string;
    number: number;
    prKey: string;
    expanded?: boolean;
    showButton?: boolean;
    showPanel?: boolean;
    ontoggle?: ((expanded: boolean) => void) | undefined;
  }

  let {
    status: _status,
    checksJSON,
    detailLoaded,
    detailSyncing,
    owner,
    name,
    number,
    prKey,
    expanded = $bindable(false),
    showButton = true,
    showPanel = true,
    ontoggle,
  }: Props = $props();

  const instanceId = ++_ciStatusInstanceCounter.value;
  const SHOW_MORE_THRESHOLD = 8;

  const parsed = $derived(parseCIChecks(checksJSON));
  const parseError = $derived(parsed.error);
  const bucketed = $derived(bucketCIChecks(parsed.checks));
  const hasCheckBucket = $derived(parseError === null && bucketed.all.length > 0);
  const isUnavailable = $derived(parseError !== null);
  const shouldRender = $derived(hasCheckBucket || isUnavailable);

  const pendingAnimated = $derived(!prefersReducedMotion());

  const expandedSections = $state<Record<"passed" | "skipped", boolean>>({
    passed: false,
    skipped: false,
  });

  $effect(() => {
    // Referencing prKey registers the dependency so this fires on PR
    // navigation. Reset the per-PR expansion state so a fresh PR starts
    // collapsed.
    void prKey;
    expandedSections.passed = false;
    expandedSections.skipped = false;
  });

  $effect(() => {
    if (bucketed.unknown.length > 0) {
      warnOnUnknownConclusions(bucketed.unknown, {
        repo: `${owner}/${name}`,
        number,
      });
    }
  });

  $effect(() => {
    if (parseError !== null) {
      warnOnMalformedCIChecksJSON(checksJSON, parseError, {
        repo: `${owner}/${name}`,
        number,
      });
    }
  });

  const passedVisible = $derived(
    expandedSections.passed
      ? bucketed.passed
      : bucketed.passed.slice(0, SHOW_MORE_THRESHOLD),
  );
  const skippedVisible = $derived(
    expandedSections.skipped
      ? bucketed.skipped
      : bucketed.skipped.slice(0, SHOW_MORE_THRESHOLD),
  );

  function formatDuration(seconds: number | undefined): string {
    if (seconds === undefined || seconds < 0 || !Number.isFinite(seconds)) {
      return "";
    }
    const wholeSeconds = Math.floor(seconds);
    if (wholeSeconds < 60) return `${wholeSeconds}s`;
    const minutes = Math.floor(wholeSeconds / 60);
    const remainingSeconds = wholeSeconds % 60;
    if (minutes < 60) {
      return remainingSeconds === 0
        ? `${minutes}m`
        : `${minutes}m ${remainingSeconds}s`;
    }
    const hours = Math.floor(minutes / 60);
    const remainingMinutes = minutes % 60;
    return remainingMinutes === 0 ? `${hours}h` : `${hours}h ${remainingMinutes}m`;
  }

  function toggleExpanded(): void {
    const next = !expanded;
    if (ontoggle) {
      ontoggle(next);
      return;
    }
    expanded = next;
  }
</script>

{#snippet rowIcon(bucket: CIBucket)}
  {#if bucket === "failed"}
    <XIcon size={16} strokeWidth={2.6} class="row-icon row-icon-red" />
  {:else if bucket === "pending"}
    {#if pendingAnimated}
      <span class="spin row-icon row-icon-amber" aria-hidden="true">
        <LoaderCircleIcon size={14} strokeWidth={2.4} />
      </span>
    {:else}
      <DotIcon size={20} class="row-icon row-icon-amber" />
    {/if}
  {:else if bucket === "passed"}
    <CheckIcon size={16} strokeWidth={2.6} class="row-icon row-icon-green" />
  {:else if bucket === "skipped"}
    <MinusIcon size={16} strokeWidth={2.6} class="row-icon row-icon-muted" />
  {:else}
    <HelpCircleIcon size={14} strokeWidth={2.4} class="row-icon row-icon-purple" />
  {/if}
{/snippet}

{#snippet checkRow(check: CICheck, bucket: CIBucket)}
  {@const duration = formatDuration(check.duration_seconds)}
  {#if check.url}
    <a
      class="ci-row"
      href={check.url}
      target="_blank"
      rel="noopener noreferrer"
    >
      <span class="ci-icon">{@render rowIcon(bucket)}</span>
      <span class="ci-name">{check.name}</span>
      {#if duration}
        <span class="ci-duration">{duration}</span>
      {/if}
      {#if check.app}
        <span class="ci-app">{check.app}</span>
      {/if}
      <span class="ci-arrow">→</span>
    </a>
  {:else}
    <div class="ci-row ci-row--static">
      <span class="ci-icon">{@render rowIcon(bucket)}</span>
      <span class="ci-name">{check.name}</span>
      {#if duration}
        <span class="ci-duration">{duration}</span>
      {/if}
      {#if check.app}
        <span class="ci-app">{check.app}</span>
      {/if}
    </div>
  {/if}
{/snippet}

{#snippet sectionBlock(
  bucket: CIBucket,
  heading: string,
  headingClass: string,
  rows: CICheck[],
)}
  <div class="ci-section ci-section-{bucket}" data-testid="ci-section-{bucket}">
    <div class="ci-section-heading {headingClass}">{heading} ({bucketed[bucket].length})</div>
    {#each rows as check (check)}
      {@render checkRow(check, bucket)}
    {/each}
  </div>
{/snippet}

{#if shouldRender}
  <div class="ci-status">
    {#if showButton}
      {#if isUnavailable && parseError !== null}
        <span class="ci-unavailable-wrap">
          <span
            class="chip chip--md chip--muted ci-chip-unavailable"
            role="button"
            tabindex="0"
            aria-disabled="true"
            aria-describedby="ci-unavailable-desc-{instanceId}"
            aria-label="CI unavailable: {safeDiagnosticText(parseError)}"
            data-testid="ci-chip"
            title="CI unavailable: {safeDiagnosticText(parseError)}"
          >CI: unavailable</span>
          <span
            class="sr-only"
            id="ci-unavailable-desc-{instanceId}"
          >CI unavailable: {safeDiagnosticText(parseError)}</span>
          <span
            class="ci-unavailable-popover"
            data-testid="ci-unavailable-popover"
          >CI unavailable: {safeDiagnosticText(parseError)}</span>
        </span>
      {:else}
        <Chip
          interactive={true}
          tone="neutral"
          ariaLabel={composeAriaLabel(bucketed)}
          dataTestid="ci-chip"
          onclick={toggleExpanded}
          title={expanded ? "Collapse CI checks" : "Expand CI checks"}
          {expanded}
        >
          <span class="ci-label">CI</span>
          <CITokenCluster {bucketed} size="default" />
          <ChevronDownIcon
            class={["chip-chevron", expanded && "chip-chevron--open"].filter(Boolean).join(" ")}
            size={12}
            strokeWidth={2.4}
            aria-hidden="true"
          />
        </Chip>
      {/if}
    {/if}

    {#if showPanel && expanded && !isUnavailable}
      <div class="ci-collapse">
        {#if !detailLoaded}
          {#if detailSyncing}
            <div class="loading-placeholder">
              <span class="sync-spinner" aria-hidden="true">
                <LoaderCircleIcon size={14} strokeWidth={2} />
              </span>
              Loading checks...
            </div>
          {:else}
            <div class="loading-placeholder">Detail not yet loaded</div>
          {/if}
        {:else if bucketed.all.length > 0}
          <div class="ci-checks">
            <div class="ci-summary">
              {bucketed.all.length} checks{#if bucketed.longestCompletedDurationSeconds !== undefined}
                <!-- eslint-disable-next-line svelte/no-useless-mustaches -->
                {" "}· longest {formatDuration(bucketed.longestCompletedDurationSeconds)}
              {/if}
            </div>
            {#if bucketed.failed.length > 0}
              {@render sectionBlock("failed", "Failed", "ci-section-heading--red", bucketed.failed)}
            {/if}
            {#if bucketed.pending.length > 0}
              {@render sectionBlock("pending", "Pending", "ci-section-heading--amber", bucketed.pending)}
            {/if}
            {#if bucketed.unknown.length > 0}
              {@render sectionBlock("unknown", "Unknown", "ci-section-heading--purple", bucketed.unknown)}
            {/if}
            {#if bucketed.passed.length > 0}
              {@render sectionBlock("passed", "Passed", "ci-section-heading--green", passedVisible)}
              {#if bucketed.passed.length > SHOW_MORE_THRESHOLD}
                <button
                  type="button"
                  class="ci-show-toggle"
                  onclick={() => { expandedSections.passed = !expandedSections.passed; }}
                >
                  {#if expandedSections.passed}
                    Show fewer passed
                  {:else}
                    Show {bucketed.passed.length - SHOW_MORE_THRESHOLD} more passed
                  {/if}
                </button>
              {/if}
            {/if}
            {#if bucketed.skipped.length > 0}
              {@render sectionBlock("skipped", "Skipped", "ci-section-heading--muted", skippedVisible)}
              {#if bucketed.skipped.length > SHOW_MORE_THRESHOLD}
                <button
                  type="button"
                  class="ci-show-toggle"
                  onclick={() => { expandedSections.skipped = !expandedSections.skipped; }}
                >
                  {#if expandedSections.skipped}
                    Show fewer skipped
                  {:else}
                    Show {bucketed.skipped.length - SHOW_MORE_THRESHOLD} more skipped
                  {/if}
                </button>
              {/if}
            {/if}
          </div>
        {/if}
      </div>
    {/if}
  </div>
{/if}

<style>
  .ci-status {
    display: contents;
  }

  .ci-label {
    margin-right: 2px;
    line-height: 1;
    display: inline-flex;
    align-items: center;
  }

  :global(.chip-chevron) {
    flex-shrink: 0;
    vertical-align: middle;
    transition: transform 0.15s;
  }

  :global(.chip-chevron--open) {
    transform: rotate(180deg);
  }

  .ci-collapse {
    flex-basis: 100%;
    width: 100%;
    min-width: 0;
    margin-top: 4px;
  }

  .ci-checks {
    display: flex;
    flex-direction: column;
    width: 100%;
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    overflow: auto;
    flex-shrink: 0;
    max-height: min(340px, 50vh);
  }

  .ci-summary {
    font-size: var(--font-size-2xs);
    font-weight: 600;
    color: var(--text-muted);
    padding: 8px 12px 6px;
    border-bottom: 1px solid var(--border-muted);
    font-variant-numeric: tabular-nums;
  }

  .ci-section + .ci-section {
    border-top: 1px solid var(--border-muted);
  }

  .ci-section-heading {
    font-size: var(--font-size-2xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding: 6px 12px 4px;
    color: var(--text-muted);
  }

  .ci-section-heading--red {
    color: var(--accent-red);
  }

  .ci-section-heading--amber {
    color: var(--accent-amber);
  }

  .ci-section-heading--green {
    color: var(--accent-green);
  }

  .ci-section-heading--muted {
    color: var(--text-muted);
  }

  .ci-section-heading--purple {
    color: var(--accent-purple);
  }

  .ci-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px;
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    text-decoration: none;
  }

  .ci-row:hover {
    background: var(--bg-surface-hover);
    text-decoration: none;
  }

  .ci-row--static {
    cursor: default;
  }

  .ci-row--static:hover {
    background: transparent;
  }

  .ci-row + .ci-row {
    border-top: 1px solid var(--border-muted);
  }

  .ci-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 16px;
  }

  :global(.row-icon-red)    { color: var(--accent-red); }
  :global(.row-icon-amber)  { color: var(--accent-amber); }
  :global(.row-icon-green)  { color: var(--accent-green); }
  :global(.row-icon-muted)  { color: var(--text-muted); }
  :global(.row-icon-purple) { color: var(--accent-purple); }

  .ci-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .ci-app {
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .ci-duration {
    font-variant-numeric: tabular-nums;
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .ci-arrow {
    color: var(--text-muted);
    flex-shrink: 0;
    font-size: var(--font-size-sm);
  }

  .ci-show-toggle {
    align-self: flex-start;
    margin: 4px 12px 8px;
    padding: 2px 8px;
    background: transparent;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    font-size: var(--font-size-2xs);
    cursor: pointer;
  }

  .ci-show-toggle:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .loading-placeholder {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    background: var(--bg-inset);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    padding: 10px 12px;
    white-space: nowrap;
  }

  .sync-spinner {
    display: inline-flex;
    animation: spin 0.9s linear infinite;
  }

  .spin {
    display: inline-flex;
    animation: spin 0.9s linear infinite;
  }

  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }

  .ci-unavailable-wrap {
    position: relative;
    display: inline-flex;
  }

  .ci-unavailable-popover {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    padding: 4px 8px;
    border-radius: 4px;
    background: var(--bg-surface);
    border: 1px solid var(--border-muted);
    color: var(--text-primary);
    font-size: var(--font-size-xs);
    box-shadow: var(--shadow-sm);
    max-width: 320px;
    white-space: normal;
    overflow-wrap: anywhere;
    opacity: 0;
    visibility: hidden;
    transition: opacity 0.12s;
    pointer-events: none;
  }

  .ci-chip-unavailable:hover + .sr-only + .ci-unavailable-popover,
  .ci-chip-unavailable:focus + .sr-only + .ci-unavailable-popover,
  .ci-chip-unavailable:focus-visible + .sr-only + .ci-unavailable-popover {
    opacity: 1;
    visibility: visible;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
</style>
