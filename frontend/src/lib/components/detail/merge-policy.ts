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

/**
 * Whether the pull request must catch up with its base before it can merge.
 * GitHub reports "behind" when a required check demands an up-to-date branch,
 * and GitLab when the merge request must be rebased. Other providers never
 * report it. A queue target is tested against the latest base by the queue.
 */
export function behindBlocksMerge(pr: { State: string; MergeableState: string }, queueTarget: boolean): boolean {
  return pr.State === "open" && pr.MergeableState === "behind" && !queueTarget;
}

/** Whether GitHub requires a review, the only provider that reports this. */
export function reviewIsRequired(pr: { ReviewDecision: string }): boolean {
  return pr.ReviewDecision.trim().toUpperCase() === "REVIEW_REQUIRED";
}

/** How branch protection and repository rules treat the merge credential's merge. */
export type MergeRules = "clear" | "blocked_bypassable" | "blocked";
export type MergeCI = "pending" | "passed" | "failed";

/**
 * The rules as the credential that merges sees them, which can be clear where
 * the sync credential sees blocked. They are read, and so apply, only while
 * the synced provider state is blocked. Until the merge credential's answer
 * arrives (""), providers with the merge_credential_rules capability offer the
 * merge without a bypass: the sync credential cannot speak for the merge
 * credential, and the provider refuses a merge its rules block. Elsewhere the
 * synced blocked state decides.
 */
export function mergeRules(
  pr: { State: string; MergeableState: string; merge_rules: string },
  mergeCredentialRules: boolean,
): MergeRules {
  if (!providerBlocksMerge(pr)) return "clear";
  return (pr.merge_rules || (mergeCredentialRules ? "clear" : "blocked")) as MergeRules;
}

export function mergeCI(pr: { CIStatus: string; CIChecksJSON: string }): MergeCI {
  if (ciStatusHasFailed(pr.CIStatus)) return "failed";
  return shouldDeferMergeForCI(pr.CIStatus, pr.CIChecksJSON) ? "pending" : "passed";
}

export interface MergeActionState {
  /** "Merge after CI" queues a merge that needs no bypass tick. */
  mergeAfterCI: boolean;
  /** An immediate merge is allowed, needs the bypass tick, or is unavailable. */
  immediate: "allowed" | "needs_bypass" | "unavailable";
  /** Why the merge button is disabled, or undefined while it is enabled. */
  disabledReason: string | undefined;
  /** Why the list menu, which has no bypass checkbox, cannot merge. */
  menuDisabledReason: string | undefined;
}

/**
 * The merge button and window states (context/deferred-merge.md). Pending CI
 * offers "Merge after CI" whatever the rules, because GitHub reports running
 * required checks as blocked. Callers keep their own handling of failed CI.
 */
export function mergeActionState(ci: MergeCI, rules: MergeRules, queueTarget: boolean): MergeActionState {
  const immediate =
    queueTarget || rules === "clear" ? "allowed" : rules === "blocked_bypassable" ? "needs_bypass" : "unavailable";
  const mergeAfterCI = ci === "pending";
  const disabledReason =
    !mergeAfterCI && immediate === "unavailable" ? "Blocked by branch protection or repository rules" : undefined;
  const menuDisabledReason =
    disabledReason ??
    (!mergeAfterCI && immediate === "needs_bypass"
      ? "Needs a bypass of branch protection or repository rules; merge it from the pull request page"
      : undefined);
  return { mergeAfterCI, immediate, disabledReason, menuDisabledReason };
}

/**
 * Whether the required review stops the merge credential now. A credential that
 * the rules do not block, and a queue target, can still merge.
 */
export function reviewBlocksMerge(
  pr: { State: string; ReviewDecision: string },
  merge: MergeActionState | undefined,
): boolean {
  return pr.State === "open" && reviewIsRequired(pr) && merge !== undefined && merge.immediate !== "allowed";
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
