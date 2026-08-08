import type { IssueDetail, PullDetail } from "../../api/types.js";
import { canonicalProvider, resolvedPlatformHost } from "../../api/provider-routes.js";

/** The repo-scoped identity every detail response has to agree with. */
export interface DetailRefLike {
  provider: string;
  platformHost?: string | undefined;
  owner: string;
  name: string;
  repoPath: string;
  number: number;
}

/**
 * Whether a loaded detail belongs to the given selection.
 *
 * A stale detail must never be treated as the selection's: it would claim
 * another item's inline workspace and render another item's diff. Provider
 * aliases and an omitted default host are the same identity, so both sides are
 * canonicalized before comparison.
 */
function repoIdentityMatches(
  detail: {
    repo_owner: string;
    repo_name: string;
    repo?: { provider?: string; platform_host?: string; repo_path?: string };
  },
  ref: DetailRefLike,
): boolean {
  return (
    detail.repo_owner === ref.owner &&
    detail.repo_name === ref.name &&
    canonicalProvider(detail.repo?.provider ?? "") === canonicalProvider(ref.provider) &&
    resolvedPlatformHost(ref.provider, detail.repo?.platform_host) ===
      resolvedPlatformHost(ref.provider, ref.platformHost) &&
    detail.repo?.repo_path === ref.repoPath
  );
}

export function pullDetailMatchesRef(detail: PullDetail | null | undefined, ref: DetailRefLike | null): boolean {
  if (!detail || !ref) return false;
  return detail.merge_request.Number === ref.number && repoIdentityMatches(detail, ref);
}

export function issueDetailMatchesRef(detail: IssueDetail | null | undefined, ref: DetailRefLike | null): boolean {
  if (!detail || !ref) return false;
  return detail.issue.Number === ref.number && repoIdentityMatches(detail, ref);
}
