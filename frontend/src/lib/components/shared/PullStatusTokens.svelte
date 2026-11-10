<script lang="ts">
  import { Spinner } from "@kenn-io/kit-ui";
  import type { CIBucketedChecks } from "../../utils/ci-buckets.js";
  import CITokenCluster, { composeAriaLabel } from "./CITokenCluster.svelte";
  import CircleAlertIcon from "@lucide/svelte/icons/circle-alert";
  import CircleCheckBigIcon from "@lucide/svelte/icons/circle-check-big";
  import ClockIcon from "@lucide/svelte/icons/clock";
  import Layers2Icon from "@lucide/svelte/icons/layers-2";
  import ListOrderedIcon from "@lucide/svelte/icons/list-ordered";
  import MessageSquareIcon from "@lucide/svelte/icons/message-square";
  import OctagonXIcon from "@lucide/svelte/icons/octagon-x";

  // The status icons of one pull request: review, merge in flight, merge
  // after CI, merge queue, stack, CI, merge conflict, and comments. Rendered
  // without a wrapper so the host row's flex gap spaces the icons. Pull request
  // rows reach this through PullStatusIcons; workspace rows pass the fields
  // their list carries.
  interface Props {
    reviewDecision?: string | null | undefined;
    merging?: boolean;
    mergeAfterCI?: boolean;
    mergeQueueLabel?: string | undefined;
    mergeQueueRemoved?: boolean;
    stack?: { position: number; size: number } | undefined;
    bucketed: CIBucketedChecks;
    ciError?: string | null;
    conflict?: boolean;
    commentCount?: number;
  }

  const {
    reviewDecision = "",
    merging = false,
    mergeAfterCI = false,
    mergeQueueLabel = undefined,
    mergeQueueRemoved = false,
    stack = undefined,
    bucketed,
    ciError = null,
    conflict = false,
    commentCount = 0,
  }: Props = $props();

  const reviewIndicator = $derived.by(
    ():
      | { kind: "approved"; label: string }
      | { kind: "changes-requested"; label: string }
      | null => {
      const decision = (reviewDecision ?? "").trim().toUpperCase();
      if (decision === "APPROVED") {
        return { kind: "approved", label: "PR approved" };
      }
      if (decision === "CHANGES_REQUESTED") {
        return { kind: "changes-requested", label: "Changes requested" };
      }
      return null;
    },
  );
</script>

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
{#if mergeAfterCI}
  <span class="merge-after-ci-indicator" aria-label="Merges after CI" title="Merges after CI">
    <ClockIcon size={13} strokeWidth={2.2} aria-hidden="true" />
  </span>
{/if}
{#if mergeQueueLabel}
  <span
    class={["merge-queue-indicator", mergeQueueRemoved ? "merge-queue-indicator--removed" : "merge-queue-indicator--queued"]}
    aria-label={mergeQueueLabel}
    title={mergeQueueLabel}
  >
    <ListOrderedIcon size={13} strokeWidth={2.2} aria-hidden="true" />
  </span>
{/if}
{#if stack}
  <span
    class="stack-indicator"
    aria-label={`Stacked: ${stack.position}/${stack.size}`}
    title={`Stacked: ${stack.position}/${stack.size}`}
  >
    <Layers2Icon size={13} strokeWidth={2.2} aria-hidden="true" />
    <span class="stack-indicator-count" aria-hidden="true">{stack.position}/{stack.size}</span>
  </span>
{/if}
{#if ciError !== null}
  <span
    class="ci ci-unavailable"
    data-testid="ci-token-unavailable"
    title={`CI unavailable: ${ciError}`}
    aria-hidden="true"
  >
    <CircleAlertIcon size={10} strokeWidth={2.5} />
  </span>
  <span class="kit-sr-only">CI unavailable: {ciError}</span>
{:else if bucketed.all.length > 0}
  <span class="ci" aria-hidden="true">
    <CITokenCluster {bucketed} size="compact" pendingStyle="static" />
  </span>
  <span class="kit-sr-only">{composeAriaLabel(bucketed)}</span>
{/if}
{#if conflict}
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
{#if commentCount > 0}
  <span
    class="comment-indicator"
    aria-label={`${commentCount} ${commentCount === 1 ? "comment" : "comments"}`}
    title={`${commentCount} ${commentCount === 1 ? "comment" : "comments"}`}
  >
    <MessageSquareIcon size={11} strokeWidth={2.2} aria-hidden="true" />
    <span class="comment-indicator-count" aria-hidden="true">{commentCount}</span>
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

  .merge-after-ci-indicator {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
    color: var(--accent-purple);
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

  .comment-indicator {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    flex: 0 0 auto;
    color: var(--text-muted);
  }

  .comment-indicator-count {
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
