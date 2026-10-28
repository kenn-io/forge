export function ciChecksHavePending(checksJSON: string): boolean {
  if (!checksJSON) return false;
  try {
    const checks = JSON.parse(checksJSON) as Array<{ status?: string }>;
    return checks.some((check) => check.status !== "completed");
  } catch {
    return false;
  }
}

function ciStatusIsPending(status: string): boolean {
  return ["pending", "in_progress", "queued"].includes(status.toLowerCase());
}

export function ciStatusHasFailed(status: string): boolean {
  return ["failure", "failed", "error", "cancelled", "canceled", "timed_out"].includes(status.toLowerCase());
}

export function shouldDeferMergeForCI(status: string, checksJSON: string): boolean {
  // The backend rejects a deferred merge once aggregate CI has failed, so a
  // failed pipeline with a check still running must use the normal merge path
  // rather than route to the deferred endpoint that would 409.
  if (ciStatusHasFailed(status)) return false;
  return ciStatusIsPending(status) || ciChecksHavePending(checksJSON);
}

/**
 * Whether the provider refuses to merge the pull request right now. GitHub and
 * GitLab report "blocked" when branch protection or repository rules stop the
 * merge, including required checks that are pending or have not reported at
 * all. A check that has not reported has no row in the synced checks, so this
 * state is the only signal for it. Providers without the signal never report
 * "blocked".
 */
export function providerBlocksMerge(pr: { State: string; MergeableState: string }): boolean {
  return pr.State === "open" && pr.MergeableState === "blocked";
}

/** The commit title and message a merge starts from, before the user edits them. */
export function defaultMergeCommit(pr: {
  number: number;
  title: string;
  body: string;
  author: string;
  authorDisplayName: string;
}): { title: string; message: string } {
  const coAuthor = `Co-authored-by: ${pr.authorDisplayName || pr.author} <${pr.author}@users.noreply.github.com>`;
  return {
    title: `${pr.title} (#${pr.number})`,
    message: pr.body ? `${pr.body}\n\n${coAuthor}` : coAuthor,
  };
}
