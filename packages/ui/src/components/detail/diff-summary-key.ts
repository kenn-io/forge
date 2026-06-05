import type { PullDetail, PullRequest } from "../../api/types.js";

export function buildDiffSummaryKey(
  owner: string,
  name: string,
  number: number,
  detail: Pick<
    PullDetail,
    "platform_head_sha" | "platform_base_sha" | "diff_head_sha" | "merge_base_sha"
  >,
  pr: Pick<PullRequest, "UpdatedAt" | "Additions" | "Deletions">,
): string {
  const revision = diffSummaryRevision(detail, pr);
  return `${owner}/${name}#${number}#${revision}`;
}

function diffSummaryRevision(
  detail: Pick<
    PullDetail,
    "platform_head_sha" | "platform_base_sha" | "diff_head_sha" | "merge_base_sha"
  >,
  pr: Pick<PullRequest, "UpdatedAt" | "Additions" | "Deletions">,
): string {
  if (detail.diff_head_sha && detail.merge_base_sha) {
    return `platform:${detail.platform_base_sha}:${detail.platform_head_sha}:diff:${detail.merge_base_sha}:${detail.diff_head_sha}`;
  }

  if (detail.platform_head_sha || detail.platform_base_sha) {
    return `platform:${detail.platform_base_sha}:${detail.platform_head_sha}`;
  }

  return `stats:${pr.UpdatedAt}:${pr.Additions}:${pr.Deletions}`;
}
