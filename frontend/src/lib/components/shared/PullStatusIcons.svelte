<script lang="ts">
  import { Spinner } from "@kenn-io/kit-ui";
  import type { PullRequest } from "../../api/types.js";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import { isPullMerging } from "../../stores/pull-merges.svelte.js";
  import { parseCIChecks, bucketCIChecks, safeDiagnosticText } from "../../utils/ci-buckets.js";
  import {
    warnOnUnknownConclusions,
    warnOnMalformedCIChecksJSON,
  } from "../../utils/ci-buckets-warn.js";
  import CITokenCluster, { composeAriaLabel } from "./CITokenCluster.svelte";
  import CircleAlertIcon from "@lucide/svelte/icons/circle-alert";
  import CircleCheckBigIcon from "@lucide/svelte/icons/circle-check-big";
  import Layers2Icon from "@lucide/svelte/icons/layers-2";
  import ListOrderedIcon from "@lucide/svelte/icons/list-ordered";
  import OctagonXIcon from "@lucide/svelte/icons/octagon-x";
  import WorkspaceIndicator from "./WorkspaceIndicator.svelte";
  import { mergeQueueIndicatorLabel } from "../detail/merge-queue.js";

  // The pull request status icons every PR row shows: workspace, review,
  // merge in flight, merge queue, stack, CI, and merge conflict. Rendered without a wrapper so
  // the host row's flex gap spaces the icons.
  const { pr }: { pr: PullRequest } = $props();

  const reviewDecision = $derived(pr.ReviewDecision.trim().toUpperCase());
  const reviewIndicator = $derived.by(
    ():
      | { kind: "approved"; label: string }
      | { kind: "changes-requested"; label: string }
      | null => {
      if (reviewDecision === "APPROVED") {
        return { kind: "approved", label: "PR approved" };
      }
      if (reviewDecision === "CHANGES_REQUESTED") {
        return { kind: "changes-requested", label: "Changes requested" };
      }
      return null;
    },
  );

  const merging = $derived(
    isPullMerging(
      { provider: pr.repo.provider, platformHost: pr.repo.platform_host, repositoryKey: repositoryKeyFromWire(pr.repo) },
      pr.Number,
    ),
  );

  const mergeQueueLabel = $derived(mergeQueueIndicatorLabel(pr.merge_queue, pr.merge_queue_removal));

  const parsed = $derived(parseCIChecks(pr.CIChecksJSON));
  const bucketed = $derived(bucketCIChecks(parsed.checks));

  $effect(() => {
    if (parsed.error !== null) {
      warnOnMalformedCIChecksJSON(pr.CIChecksJSON, parsed.error, {
        repo: `${pr.repo_owner}/${pr.repo_name}`,
        number: pr.Number,
      });
    }
  });

  $effect(() => {
    if (bucketed.unknown.length > 0) {
      warnOnUnknownConclusions(bucketed.unknown, {
        repo: `${pr.repo_owner}/${pr.repo_name}`,
        number: pr.Number,
      });
    }
  });
</script>

{#if pr.workspace}
  <WorkspaceIndicator status={pr.workspace.status} />
{/if}
{#if reviewIndicator}
  <span
    class={["review-indicator", `review-indicator--${reviewIndicator.kind}`]}
    aria-label={reviewIndicator.label}
    title={reviewIndicator.label}
  >
    {#if reviewIndicator.kind === "approved"}
      <CircleCheckBigIcon size={13} strokeWidth={2.2} aria-hidden="true" />
    {:else}
      <OctagonXIcon size={13} strokeWidth={2.2} aria-hidden="true" />
    {/if}
  </span>
{/if}
{#if merging}
  <span class="merging-indicator">
    <span class="merging-spinner" aria-hidden="true"><Spinner size={11} /></span>
    Merging
  </span>
{/if}
{#if mergeQueueLabel}
  <span
    class={["merge-queue-indicator", pr.merge_queue ? "merge-queue-indicator--queued" : "merge-queue-indicator--removed"]}
    aria-label={mergeQueueLabel}
    title={mergeQueueLabel}
  >
    <ListOrderedIcon size={13} strokeWidth={2.2} aria-hidden="true" />
  </span>
{/if}
{#if pr.stack}
  <span
    class="stack-indicator"
    aria-label={`Stacked: ${pr.stack.position}/${pr.stack.size}`}
    title={`Stacked: ${pr.stack.position}/${pr.stack.size}`}
  >
    <Layers2Icon size={13} strokeWidth={2.2} aria-hidden="true" />
    <span class="stack-indicator-count" aria-hidden="true">{pr.stack.position}/{pr.stack.size}</span>
  </span>
{/if}
{#if parsed.error !== null}
  <span
    class="ci ci-unavailable"
    data-testid="ci-token-unavailable"
    title={`CI unavailable: ${safeDiagnosticText(parsed.error)}`}
    aria-hidden="true"
  >
    <CircleAlertIcon size={10} strokeWidth={2.5} />
  </span>
  <span class="kit-sr-only">CI unavailable: {safeDiagnosticText(parsed.error)}</span>
{:else if bucketed.all.length > 0}
  <span class="ci" aria-hidden="true">
    <CITokenCluster {bucketed} size="compact" pendingStyle="static" />
  </span>
  <span class="kit-sr-only">{composeAriaLabel(bucketed)}</span>
{/if}
{#if pr.MergeableState === "dirty"}
  <span class="conflict-icon" title="Has merge conflicts">
    <!-- git-merge-conflict icon, ISC License, Copyright (c) Lucide Icons and Contributors -->
    <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <path d="M12 6h4a2 2 0 0 1 2 2v7" />
      <path d="M6 12v9" />
      <path d="M9 3 3 9" />
      <path d="M9 9 3 3" />
      <circle cx="18" cy="18" r="3" />
    </svg>
  </span>
{/if}

<style>
  .review-indicator {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
  }

  .review-indicator--approved {
    color: var(--accent-green);
  }

  .review-indicator--changes-requested {
    color: var(--accent-red);
  }

  .merging-indicator {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    flex: 0 0 auto;
    color: var(--accent-purple);
    font-size: var(--font-size-2xs);
    font-weight: 600;
    line-height: 1;
  }

  .merging-spinner {
    display: inline-flex;
  }

  .merge-queue-indicator {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
  }

  .merge-queue-indicator--queued {
    color: var(--accent-amber);
  }

  .merge-queue-indicator--removed {
    color: var(--accent-red);
  }

  .stack-indicator {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    flex: 0 0 auto;
    color: var(--text-muted);
  }

  .stack-indicator-count {
    font-size: var(--font-size-2xs);
    font-weight: 500;
    font-variant-numeric: tabular-nums;
    line-height: 1;
  }

  .ci-unavailable {
    color: var(--state-warn, var(--accent-amber, #c08a2a));
    opacity: 0.85;
  }

  .ci {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    gap: var(--space-2);
  }

  :global(.mobile-main) .ci {
    gap: var(--space-1);
  }

  :global(.mobile-main) .stack-indicator-count {
    font-size: var(--font-size-sm);
    line-height: 1.35;
  }

  .conflict-icon {
    display: flex;
    align-items: center;
    color: var(--accent-amber);
    flex-shrink: 0;
  }
</style>
