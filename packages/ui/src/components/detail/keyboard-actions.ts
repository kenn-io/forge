/**
 * Shared canX/runX closures for the four PR-detail mutating actions.
 *
 * The PR-detail buttons (ApproveButton, ApproveWorkflowsButton,
 * ReadyForReviewButton, and the in-PullDetail merge trigger) all gate
 * their render or click on the same conditions and call the same
 * sequence of mutations + refreshes that this file centralizes. The
 * keyboard palette (Task 26) wires these same closures into command
 * entries so palette execution and button clicks go through identical
 * logic.
 *
 * Inventory of the existing buttons captured before extraction
 * ============================================================
 *
 * 1. ApproveButton.svelte
 *    - Render guard: rendered unconditionally by the button itself; the
 *      caller (PullDetail) wraps it in `{#if pr.State === "open" &&
 *      capabilities.review_mutation}` and passes `disabled={stalePR}`.
 *      The mutation only fires once the inline approve form has been
 *      expanded (`expanded = true`) and the user clicks the inner
 *      "Approve" button, but the gate this file replaces is the
 *      "should the action be available?" question, not the inline UX.
 *    - Click handler (`handleApprove`):
 *        if disabled -> return
 *        submitting = true
 *        POST providerItemPath("pulls", ref, "/approve") with
 *          body: { body: body.trim() }
 *        on error -> throw `error.detail ?? error.title ??
 *                            "failed to approve pull request"`
 *        on success:
 *          body = ""; expanded = false;
 *          await detail.loadDetail(owner, name, number, ref)
 *          await pulls.loadPulls()
 *        finally -> submitting = false
 *    - Input props: owner, name, number, provider, platformHost,
 *      repoPath, size, disabled.
 *    - Endpoint: POST /pulls/.../approve.
 *    - Refresh path: detail.loadDetail(...) + pulls.loadPulls().
 *    - Toast/flash: none. Errors render inline as the button's own
 *      `let error = $state<string|null>(null)` text.
 *
 * 2. ApproveWorkflowsButton.svelte
 *    - Render guard: rendered when caller passes it in (PullDetail
 *      wraps in `{#if capabilities.workflow_approval &&
 *      workflowApproval?.checked && workflowApproval.required}`); also
 *      `disabled={stalePR}`.
 *    - Click handler (`handleApproveWorkflows`):
 *        if disabled -> return
 *        submitting = true
 *        POST providerItemPath("pulls", ref, "/approve-workflows")
 *          with no body
 *        on error -> throw `requestError.detail ?? requestError.title
 *                           ?? "failed to approve workflows"`
 *        on success:
 *          await detail.refreshDetailOnly(owner, name, number, ref)
 *          await pulls.loadPulls()
 *          oncompleted?.()
 *        finally -> submitting = false
 *    - Input props: owner, name, number, provider, platformHost,
 *      repoPath, count, size, disabled, oncompleted.
 *    - Endpoint: POST /pulls/.../approve-workflows.
 *    - Refresh path: detail.refreshDetailOnly(...) + pulls.loadPulls()
 *      + caller-supplied oncompleted callback.
 *    - Toast/flash: none. Errors render inline as the button's own
 *      `error` state text.
 *
 * 3. ReadyForReviewButton.svelte
 *    - Render guard: rendered when caller passes it in (PullDetail
 *      wraps in `{#if pr.IsDraft && capabilities.ready_for_review}`);
 *      also `disabled={stalePR}`.
 *    - Click handler (`handleReadyForReview`):
 *        if disabled -> return
 *        submitting = true
 *        POST providerItemPath("pulls", ref, "/ready-for-review")
 *          with no body
 *        on error -> throw `error.detail ?? error.title ?? "failed to
 *                           mark pull request ready for review"`
 *        on success:
 *          await detail.loadDetail(owner, name, number, ref)
 *          await pulls.loadPulls()
 *          oncompleted?.()
 *        on catch:
 *          if message includes "ready for review" + "404 Not Found"
 *          (server already flipped state and the local cache is
 *          stale): try a best-effort detail.loadDetail + pulls.loadPulls
 *          to converge state, swallowing any refresh error so the
 *          original mutation error survives.
 *        finally -> submitting = false
 *    - Input props: owner, name, number, provider, platformHost,
 *      repoPath, size, disabled, oncompleted.
 *    - Endpoint: POST /pulls/.../ready-for-review.
 *    - Refresh path: detail.loadDetail(...) + pulls.loadPulls() +
 *      caller-supplied oncompleted callback. The stale-state recovery
 *      path adds an extra detail.loadDetail + pulls.loadPulls when the
 *      mutation reports a 404 because the underlying PR is no longer a
 *      draft.
 *    - Toast/flash: none. Errors render inline as the button's own
 *      `error` state text.
 *
 * 4. Merge open trigger (lives in PullDetail.svelte, lines ~866-893)
 *    - Render guard: outer `{#if repoSettings && capabilities.merge_mutation}`
 *      plus inline `disabled={stalePR || mergeDisabledByConflicts}`
 *      where `mergeDisabledByConflicts = hasMergeConflicts(pr)` and
 *      `hasMergeConflicts({State, MergeableState}) = State==="open" &&
 *      MergeableState==="dirty"`.
 *    - Click handler (sync, no mutation):
 *        if (stalePR || mergeDisabledByConflicts) return
 *        showMergeModal = true
 *        closeActionMenu()
 *      (`showMergeModal` is `let showMergeModal = $state(false)` owned
 *      by PullDetail; the click flips it to true, which renders the
 *      MergeModal component below. The modal's own onclose flips it
 *      back to false. MergeModal.svelte itself is unchanged by this
 *      task; only the trigger that opens it is extracted. The
 *      modal-frame push wired into MergeModal.svelte by an earlier
 *      task is intentionally left untouched.)
 *    - Endpoint: none on click; the actual /merge POST happens inside
 *      MergeModal once the user confirms in the modal.
 *
 * Why PullDetail.svelte is the file modified for the merge trigger
 * ----------------------------------------------------------------
 * The plan lists MergeModal.svelte as a "Modify" file, but
 * MergeModal.svelte is the modal panel itself — it does not contain a
 * trigger button that opens the modal. The trigger button lives in
 * PullDetail.svelte (it sits next to ApproveButton in the primary
 * actions row). Per the engineering directive that accompanies this
 * task, we follow where the actual click handler lives, so this commit
 * refactors PullDetail.svelte's merge trigger and leaves MergeModal.svelte
 * alone (its modal-frame push from an earlier task stays intact).
 *
 * Shape rationale
 * ---------------
 * `PRDetailActionInput` exists once and is shared by every closure
 * pair so the palette in Task 26 can construct one input from active
 * `Context` and store instances and reuse it for each command. Fields
 * are deliberately narrow: closures only see what they need to decide
 * (canX) or to mutate + refresh (runX), and never read module-level
 * state, route, or localStorage.
 *
 * `viewerCan` collapses the four per-button availability bits the
 * existing buttons already gate on into a single object. The buttons
 * today read these via the per-action wrapper conditions in
 * PullDetail.svelte (`capabilities.review_mutation`,
 * `capabilities.merge_mutation`, `capabilities.ready_for_review`,
 * `capabilities.workflow_approval && workflowApproval?.checked &&
 * workflowApproval.required`). Reusing those exact predicates here
 * keeps the closure decisions identical to what the buttons render
 * today.
 *
 * `repoSettings` is needed for `canOpenMerge` because the existing
 * trigger requires it to decide which merge methods to offer. It is
 * optional everywhere else.
 */

import type { PullRequest } from "../../api/types.js";
import { isProblem, problemConflictContext, problemConflictReason } from "../../api/problems.js";
import { providerItemPath, providerRouteParams, type ProviderRouteRef } from "../../api/provider-routes.js";
import type { MiddlemanClient } from "../../types.js";
import type { DetailStore } from "../../stores/detail.svelte.js";
import type { PullsStore } from "../../stores/pulls.svelte.js";

/** Subset of the loaded PR sufficient for canX/runX decisions. */
export type PRDetailActionPR = Pick<PullRequest, "State" | "IsDraft" | "MergeableState" | "platform_head_sha">;

/** Capabilities a viewer needs to invoke each PR-detail action. */
export interface PRDetailViewerCan {
  approve: boolean;
  merge: boolean;
  markReady: boolean;
  approveWorkflows: boolean;
}

/** Repo merge-method settings. canOpenMerge requires this to be set. */
export interface PRDetailRepoSettings {
  allowSquash: boolean;
  allowMerge: boolean;
  allowRebase: boolean;
  viewerCanMerge: boolean;
}

/** Stores the closures touch on the refresh path. */
export interface PRDetailActionStores {
  pulls: Pick<PullsStore, "loadPulls">;
  detail: Pick<DetailStore, "loadDetail" | "refreshDetailOnly">;
}

/** Shared input bundle for every PR-detail action closure pair. */
export interface PRDetailActionInput {
  pr: PRDetailActionPR;
  ref: ProviderRouteRef;
  number: number;
  viewerCan: PRDetailViewerCan;
  repoSettings: PRDetailRepoSettings | null;
  /** True iff the PR is stale (route changed mid-load, etc.). */
  stale: boolean;
  stores: PRDetailActionStores;
  client: MiddlemanClient;
  /**
   * True when the provider hard-binds mutations to the reviewed head
   * (capabilities.mutation_head_binding). Head-bound actions are
   * unavailable until the loaded detail carries reviewed_head_sha to pin.
   */
  requireHeadPin?: boolean;
  /**
   * Approve mutation body. Empty string sends an approving review
   * with no comment (matching the existing button's behavior when
   * the user submits without typing into the textarea).
   */
  approveCommentBody?: string;
  /**
   * Head commit the rendered detail was reviewed at
   * (detail.reviewed_head_sha). When set, head-bound mutations echo it
   * as expected_head_sha so the server rejects the action with a 409
   * conflict if a sync rebound the head between render and click.
   */
  expectedHeadSha?: string;
  /**
   * Invoked when the server rejects a head-bound mutation because the
   * pinned head no longer matches (stale_state) or has never been
   * synced (head_unknown). The owner of this callback is responsible
   * for refreshing the detail and gating further head-bound actions;
   * the runX closures only report the reason and rethrow.
   */
  // context carries provider side-effect detail (details.context):
  // an approval that could not be revoked, posted review text a
  // retry would repeat. Present only when the server reported one.
  onHeadConflict?: (reason: "stale_state" | "head_unknown", context?: string) => void;
  /** Owned by PullDetail; runOpenMerge flips this to true. */
  setMergeModalOpen?: (open: boolean) => void;
  /**
   * Optional cleanup hook (e.g. close-action-menu) that the existing
   * trigger called immediately after flipping the modal open state.
   */
  onAfterOpenMerge?: () => void;
  /**
   * Optional callback the runX path invokes after a successful
   * mutation + refresh — matches the existing buttons' `oncompleted`
   * prop (used by the action menu to close itself).
   */
  onCompleted?: () => void;
  /**
   * Optional error reporter. The buttons today render their own inline
   * error text and do not call this; the palette uses it to surface
   * failures via the flash store.
   */
  onError?: (msg: string) => void;
}

function hasMergeConflicts(pr: PRDetailActionPR): boolean {
  return pr.State === "open" && pr.MergeableState === "dirty";
}

function describeError(err: { detail?: string; title?: string } | undefined, fallback: string): string {
  return err?.detail ?? err?.title ?? fallback;
}

// Approve PR ----------------------------------------------------------

export function canApprovePR(input: PRDetailActionInput): boolean {
  return (
    input.pr.State === "open" &&
    input.viewerCan.approve &&
    !input.stale &&
    (!input.requireHeadPin || !!input.pr.platform_head_sha)
  );
}

export async function runApprovePR(input: PRDetailActionInput): Promise<void> {
  if (!canApprovePR(input)) return;
  const { ref, number } = input;
  const body = (input.approveCommentBody ?? "").trim();
  // Pin the approval to the head the user reviewed; the server rejects
  // the request when the synced head has moved past it. Callers that
  // build the input from the loaded detail pass expectedHeadSha; the
  // pr row's platform_head_sha is the fallback for callers that already
  // projected the reviewed head into the action input.
  const expectedHeadSha = (input.expectedHeadSha ?? input.pr.platform_head_sha ?? "").trim();
  const { error } = await input.client.POST(providerItemPath("pulls", ref, "/approve"), {
    params: { path: { ...providerRouteParams(ref), number } },
    body: {
      body,
      ...(expectedHeadSha !== "" && { expected_head_sha: expectedHeadSha }),
    },
  });
  if (error) {
    const reason = isProblem(error) ? problemConflictReason(error) : undefined;
    if (reason === "stale_state" || reason === "head_unknown") {
      const context = isProblem(error) ? problemConflictContext(error) : undefined;
      input.onHeadConflict?.(reason, context);
    }
    const msg = describeError(error, "failed to approve pull request");
    input.onError?.(msg);
    throw new Error(msg);
  }
  await input.stores.detail.loadDetail(ref.owner, ref.name, number, {
    provider: ref.provider,
    platformHost: ref.platformHost,
    repoPath: ref.repoPath,
  });
  await input.stores.pulls.loadPulls();
}

// Open the merge modal -----------------------------------------------

export function canOpenMerge(input: PRDetailActionInput): boolean {
  return (
    input.pr.State === "open" &&
    input.viewerCan.merge &&
    input.repoSettings !== null &&
    input.repoSettings.viewerCanMerge &&
    !input.stale &&
    !hasMergeConflicts(input.pr) &&
    (!input.requireHeadPin || !!input.pr.platform_head_sha)
  );
}

export function runOpenMerge(input: PRDetailActionInput): void {
  if (!canOpenMerge(input)) return;
  input.setMergeModalOpen?.(true);
  input.onAfterOpenMerge?.();
}

// Mark draft PR ready for review -------------------------------------

export function canMarkReady(input: PRDetailActionInput): boolean {
  return input.pr.State === "open" && input.pr.IsDraft === true && input.viewerCan.markReady && !input.stale;
}

function isStaleDraftRefreshSignal(message: string): boolean {
  return message.includes("ready for review") && message.includes("404 Not Found");
}

export async function runMarkReady(input: PRDetailActionInput): Promise<void> {
  if (!canMarkReady(input)) return;
  const { ref, number } = input;
  let mutationError: Error | null = null;
  try {
    const { error } = await input.client.POST(providerItemPath("pulls", ref, "/ready-for-review"), {
      params: { path: { ...providerRouteParams(ref), number } },
    });
    if (error) {
      throw new Error(describeError(error, "failed to mark pull request ready for review"));
    }
  } catch (err) {
    mutationError = err instanceof Error ? err : new Error(String(err));
  }

  if (mutationError === null) {
    await input.stores.detail.loadDetail(ref.owner, ref.name, number, {
      provider: ref.provider,
      platformHost: ref.platformHost,
      repoPath: ref.repoPath,
    });
    await input.stores.pulls.loadPulls();
    input.onCompleted?.();
    return;
  }

  if (isStaleDraftRefreshSignal(mutationError.message)) {
    try {
      await input.stores.detail.loadDetail(ref.owner, ref.name, number, {
        provider: ref.provider,
        platformHost: ref.platformHost,
        repoPath: ref.repoPath,
      });
      await input.stores.pulls.loadPulls();
    } catch {
      // Preserve the original mutation error if the stale-state
      // refresh also fails.
    }
  }
  input.onError?.(mutationError.message);
  throw mutationError;
}

// Approve pending workflows ------------------------------------------

export function canApproveWorkflows(input: PRDetailActionInput): boolean {
  return input.pr.State === "open" && input.viewerCan.approveWorkflows && !input.stale;
}

export async function runApproveWorkflows(input: PRDetailActionInput): Promise<void> {
  if (!canApproveWorkflows(input)) return;
  const { ref, number } = input;
  const { error: requestError } = await input.client.POST(providerItemPath("pulls", ref, "/approve-workflows"), {
    params: { path: { ...providerRouteParams(ref), number } },
  });
  if (requestError) {
    const msg = describeError(requestError, "failed to approve workflows");
    input.onError?.(msg);
    throw new Error(msg);
  }
  await input.stores.detail.refreshDetailOnly(ref.owner, ref.name, number, {
    provider: ref.provider,
    platformHost: ref.platformHost,
    repoPath: ref.repoPath,
  });
  await input.stores.pulls.loadPulls();
  input.onCompleted?.();
}
