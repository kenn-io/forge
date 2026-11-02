import { repositoryKeyFromWire } from "../../api/repository-key.js";
import type { PullRequest } from "../../api/types.js";
import { repoIdentityKey } from "../../utils/repo-label.js";

/** One provider merge queue: a repository's base branch and the pull requests Forge last read in or out of it. */
export interface MergeQueueGroup {
  readonly key: string;
  readonly repo: PullRequest["repo"];
  readonly branch: string;
  /** Queued pull requests in queue order; entries whose position is not reported yet come last. */
  readonly entries: readonly PullRequest[];
  /** Pull requests the queue removed without merging, most recent first. */
  readonly removals: readonly PullRequest[];
  /** The provider's page for this queue, when the pull request URLs reveal it. */
  readonly providerURL: string | undefined;
}

interface MutableGroup {
  key: string;
  repo: PullRequest["repo"];
  branch: string;
  entries: PullRequest[];
  removals: PullRequest[];
}

function entryOrder(a: PullRequest, b: PullRequest): number {
  const positionA = a.merge_queue?.position ?? 0;
  const positionB = b.merge_queue?.position ?? 0;
  if (positionA > 0 && positionB > 0 && positionA !== positionB) return positionA - positionB;
  if (positionA > 0 !== positionB > 0) return positionA > 0 ? -1 : 1;
  return (a.merge_queue?.enqueued_at ?? "").localeCompare(b.merge_queue?.enqueued_at ?? "") || a.Number - b.Number;
}

function removalOrder(a: PullRequest, b: PullRequest): number {
  return (
    (b.merge_queue_removal?.removed_at ?? "").localeCompare(a.merge_queue_removal?.removed_at ?? "") ||
    a.Number - b.Number
  );
}

// GitHub serves a branch's queue at /<owner>/<repo>/queue/<branch>, beside /pull/<n>.
function providerQueueURL(group: MutableGroup): string | undefined {
  const sample = group.entries[0] ?? group.removals[0];
  const match = sample?.URL.match(/^(https?:\/\/.+)\/pull\/\d+\/?$/);
  return match ? `${match[1]}/queue/${encodeURIComponent(group.branch)}` : undefined;
}

/**
 * Groups pull requests by the merge queue they are in or were removed from:
 * one group per repository and base branch, ordered by repository path and
 * branch. Pull requests with no queue entry or removal are left out.
 */
export function groupMergeQueues(pulls: readonly PullRequest[]): MergeQueueGroup[] {
  const groups = new Map<string, MutableGroup>();
  for (const pr of pulls) {
    if (!pr.merge_queue && !pr.merge_queue_removal) continue;
    const repoKey = repoIdentityKey({
      provider: pr.repo.provider,
      platformHost: pr.repo.platform_host,
      owner: pr.repo.owner,
      name: pr.repo.name,
      repoPath: pr.repo.repo_path,
      repositoryKey: repositoryKeyFromWire(pr.repo),
    });
    const key = JSON.stringify([repoKey, pr.BaseBranch]);
    let group = groups.get(key);
    if (!group) {
      group = { key, repo: pr.repo, branch: pr.BaseBranch, entries: [], removals: [] };
      groups.set(key, group);
    }
    if (pr.merge_queue) group.entries.push(pr);
    else group.removals.push(pr);
  }
  return [...groups.values()]
    .map((group) => ({
      key: group.key,
      repo: group.repo,
      branch: group.branch,
      entries: group.entries.sort(entryOrder),
      removals: group.removals.sort(removalOrder),
      providerURL: providerQueueURL(group),
    }))
    .sort(
      (a, b) =>
        a.repo.repo_path.localeCompare(b.repo.repo_path) ||
        a.repo.platform_host.localeCompare(b.repo.platform_host) ||
        a.branch.localeCompare(b.branch),
    );
}
