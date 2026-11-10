<script lang="ts">
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
  const { pr, showMergeQueue = true }: { pr: PullRequest; showMergeQueue?: boolean } = $props();

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
<PullStatusTokens
  reviewDecision={pr.ReviewDecision}
  {merging}
  mergeQueueLabel={showMergeQueue ? mergeQueueLabel : undefined}
  mergeQueueRemoved={!pr.merge_queue}
  stack={pr.stack}
  {bucketed}
  ciError={parsed.error === null ? null : safeDiagnosticText(parsed.error)}
  conflict={pr.MergeableState === "dirty"}
/>
