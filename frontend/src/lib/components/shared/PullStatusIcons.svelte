<script lang="ts">
  import type { Snippet } from "svelte";
  import type { PullRequest } from "../../api/types.js";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import { isPullMerging } from "../../stores/pull-merges.svelte.js";
  import { parseCIChecks, bucketCIChecks, safeDiagnosticText } from "../../utils/ci-buckets.js";
  import {
    warnOnUnknownConclusions,
    warnOnMalformedCIChecksJSON,
  } from "../../utils/ci-buckets-warn.js";
  import PullStatusTokens from "./PullStatusTokens.svelte";
  import WorkspaceIndicator from "./WorkspaceIndicator.svelte";
  import { mergeQueueIndicatorLabel } from "../detail/merge-queue.js";

  // The pull request status icons every PR row shows: the workspace indicator
  // and the PullStatusTokens for a synced pull request. Rendered without a
  // wrapper so the host row's flex gap spaces the icons. The merge queue page
  // turns off the merge queue icon, since its rows are grouped by queue already.
  // The PR table turns off the parts a person has hidden; a stack's expand control stays.
  const { pr, showMergeQueue = true, showCI = true, showReview = true, showStack = true, stackControl }: {
    pr: PullRequest;
    showMergeQueue?: boolean;
    showCI?: boolean;
    showReview?: boolean;
    showStack?: boolean;
    stackControl?: Snippet | undefined;
  } = $props();

  const merging = $derived(
    isPullMerging(
      { provider: pr.repo.provider, platformHost: pr.repo.platform_host, repositoryKey: repositoryKeyFromWire(pr.repo) },
      pr.Number,
    ),
  );

  const mergeQueueLabel = $derived(mergeQueueIndicatorLabel(pr.merge_queue, pr.merge_queue_removal));

  const parsed = $derived(parseCIChecks(pr.CIChecksJSON));
  const bucketed = $derived(bucketCIChecks(parsed.checks));
  const noChecks = bucketCIChecks([]);

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
<PullStatusTokens
  reviewDecision={showReview ? pr.ReviewDecision : ""}
  {merging}
  mergeQueueLabel={showMergeQueue ? mergeQueueLabel : undefined}
  mergeQueueRemoved={!pr.merge_queue}
  stack={showStack ? pr.stack : undefined}
  {stackControl}
  bucketed={showCI ? bucketed : noChecks}
  ciError={!showCI || parsed.error === null ? null : safeDiagnosticText(parsed.error)}
  conflict={pr.MergeableState === "dirty"}
/>
