<script lang="ts">
  import {
    Button,
    Checkbox,
    Modal,
    SegmentedControl,
    SelectDropdown,
    type SelectDropdownOption,
  } from "@kenn-io/kit-ui";
  import ShieldAlertIcon from "@lucide/svelte/icons/shield-alert";
  import { onMount, untrack } from "svelte";

  import {
    isProblem,
    problemConflictContext,
    problemConflictReason,
    type ConflictReason,
    type ProblemBody,
  } from "../../api/problems.js";
  import type { ProviderRouteRef } from "../../api/provider-routes.js";
  import type { MergeParams } from "../../api/types.js";
  import { getStores } from "../../context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";
  import type { StackContextResponse } from "../../api/generated/models/stackContextResponse.js";
  import type { StackMemberResponse } from "../../api/generated/models/stackMemberResponse.js";
  import { nativeStackMergeScope, nativeStackMergeTargets } from "./stack-merge.js";
  import type { RepositoryKey } from "../../api/repository-key.js";
  import {
    ciStatusHasFailed,
    ciStatusIsPending,
    defaultMergeCommit,
    nativeStackBelowCI,
  } from "./merge-policy.js";

  const { detail } = getStores();

  onMount(() => pushModalFrame("merge-modal", []));

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
    repositoryKey?: RepositoryKey | undefined;
    repoPath: string;
    prTitle: string;
    prBody: string;
    prAuthor: string;
    prAuthorDisplayName: string;
    allowSquash: boolean;
    allowMerge: boolean;
    allowRebase: boolean;
    /** Head commit the rendered detail showed; pinned on merge. */
    expectedHeadSha?: string | undefined;
    /** capabilities.mutation_head_binding for this repo's provider. */
    requireHeadPin?: boolean;
    routeGeneration?: number;
    /** When true, the primary action waits for currently pending CI before merging. */
    deferUntilChecksPass?: boolean;
    /** Warn before an immediate merge when aggregate CI has failed. */
    ciFailed?: boolean;
    /** Providers refuse to merge a draft, so the modal blocks it and says why. */
    isDraft?: boolean;
    /**
     * The provider refuses an immediate merge right now (branch protection or
     * repository rules). Only a deferred merge stays available.
     */
    immediateMergeBlocked?: boolean;
    /** The provider lets this merge bypass the rules that block it. */
    canBypassRules?: boolean;
    /**
     * True while a background merge is already queued for this PR. The
     * deferred action is withheld (the server would 409 on a second
     * queue) and the modal offers an immediate merge instead.
     */
    alreadyQueued?: boolean;
    /** Exact workspace to delete after a successful merge. */
    workspaceId?: string | undefined;
    /** Stack context shown when merging a pull request. */
    stackNote?: string | undefined;
    /**
     * Set when the provider owns the stack: a merge also lands every unmerged
     * pull request below its target, so the modal names them all and lets the
     * user choose the target.
     */
    nativeStack?: StackContextResponse | undefined;
    /**
     * True when the base branch uses a merge queue: submitting adds the
     * pull request to the queue, which merges it with its own configured
     * method, so method and commit text do not apply. A requested workspace
     * deletion waits for the queue's merge.
     */
    mergeQueue?: boolean;
    onclose: () => void;
    onstateconflict?: ((
      reason: Exclude<ConflictReason, "conflict">,
      context: string | undefined,
      expectedHeadSha: string,
      ref: ProviderRouteRef,
      number: number,
      routeGeneration: number,
    ) => void) | undefined;
  }

  const {
    owner, name, number, provider, platformHost, repositoryKey, repoPath, prTitle, prBody,
    prAuthor, prAuthorDisplayName,
    allowSquash, allowMerge, allowRebase,
    expectedHeadSha, requireHeadPin = false, routeGeneration = 0,
    deferUntilChecksPass = false,
    ciFailed = false,
    isDraft = false,
    immediateMergeBlocked = false,
    canBypassRules = false,
    alreadyQueued = false, workspaceId, stackNote, nativeStack, mergeQueue = false,
    onclose, onstateconflict,
  }: Props = $props();

  // The stack target starts at this pull request; choosing another merges up
  // to that one instead. The targets and their reviewed heads are captured
  // when the modal opens, as this pull request's head pin is, while their CI
  // and draft state follow detail refreshes.
  const stackTargets = untrack(() => nativeStackMergeTargets(nativeStack));
  let targetNumber = $state(untrack(() => number));
  const mergingThis = $derived(targetNumber === number);
  const pinnedTargetMember = $derived<StackMemberResponse | undefined>(
    stackTargets.find((member) => member.number === targetNumber),
  );
  function liveMember(member: StackMemberResponse): StackMemberResponse {
    return nativeStack?.members.find((candidate) => candidate.number === member.number) ?? member;
  }
  const targetMember = $derived(pinnedTargetMember && liveMember(pinnedTargetMember));
  const nativeStackMerge = $derived(nativeStackMergeScope(nativeStack, targetNumber));
  // The members below the target land with it, so their CI counts too.
  const belowCI = $derived(nativeStackBelowCI(nativeStack, targetNumber));
  const targetCIFailed = $derived(
    mergingThis ? ciFailed : ciStatusHasFailed(targetMember?.ci_status ?? ""),
  );
  const targetCIPending = $derived(
    mergingThis ? deferUntilChecksPass : ciStatusIsPending(targetMember?.ci_status ?? ""),
  );
  const landingCIFailed = $derived(targetCIFailed || belowCI.failed.length > 0);
  const landingCIPending = $derived(
    !landingCIFailed && (targetCIPending || belowCI.pending.length > 0),
  );
  const pendingNumbers = $derived(
    targetCIPending ? [...belowCI.pending, targetNumber] : belowCI.pending,
  );
  // Only this pull request's detail says what is queued and what its rules
  // block; for another target the provider decides when the merge runs.
  const targetQueued = $derived(mergingThis && alreadyQueued);
  const targetDraft = $derived(mergingThis ? isDraft : (targetMember?.is_draft ?? false));
  const targetImmediateBlocked = $derived(mergingThis && immediateMergeBlocked);
  const targetCanBypass = $derived(mergingThis && canBypassRules);

  // Offer to queue a deferred merge only when none is queued yet.
  const offerDeferredMerge = $derived(landingCIPending && !targetQueued);

  // More than one pull request lands, so the action is a stack merge.
  const stackLandingCount = $derived.by(() => {
    const count = nativeStackMerge?.landing.length ?? 0;
    return count > 1 ? count : 0;
  });

  function numberList(numbers: number[]): string {
    const refs = numbers.map(n => `#${n}`);
    if (refs.length <= 2) return refs.join(" and ");
    return `${refs.slice(0, -1).join(", ")}, and ${refs.at(-1)}`;
  }

  function modalTitle(): string {
    if (mergeQueue) return stackLandingCount ? "Add stack to merge queue" : "Add to merge queue";
    return stackLandingCount ? "Merge Stack" : "Merge Pull Request";
  }

  // Captured once when the modal opens: a background detail refresh
  // must not silently rebind the pin to a head the user has not seen
  // while the form is already on screen. If the head really moved, the
  // server rejects this stale pin and the conflict flow takes over.
  const pinnedHeadShaAtOpen = untrack(() => (expectedHeadSha ?? "").trim());
  const routeGenerationAtOpen = untrack(() => routeGeneration);

  // Another target pins the reviewed head its stack entry reports.
  const targetPin = $derived(
    mergingThis ? pinnedHeadShaAtOpen : (pinnedTargetMember?.reviewed_head_sha ?? "").trim(),
  );
  // A head-binding provider cannot merge without a pinned head; the user
  // must wait for sync and re-review before merging.
  const requireHeadPinAtOpen = untrack(() => requireHeadPin);
  const headPinMissing = $derived(requireHeadPinAtOpen && targetPin === "");

  const mergeBlocked = $derived(headPinMissing || targetDraft);
  let bypassRules = $state(false);
  const immediateAllowed = $derived(!targetImmediateBlocked || (targetCanBypass && bypassRules));
  // Only Forge's worker merges over the rules; GitHub auto-merge cannot.
  const sendsBypass = $derived(targetImmediateBlocked && targetCanBypass && bypassRules);
  // The workspace belongs to this pull request. Deleting it is offered only
  // for a merge of this pull request, whose request replaces any deletion an
  // earlier merge of it kept; a merge up to another member leaves it alone.
  const workspaceLands = $derived(workspaceId !== undefined && mergingThis);
  // The primary action merges immediately unless it queues a deferred merge.
  const primaryBlocked = $derived(mergeBlocked || (!offerDeferredMerge && !immediateAllowed));

  type Method = "merge" | "squash" | "rebase";
  type MethodOption = { value: Method; label: string };
  function buildMethods(): MethodOption[] {
    const out: MethodOption[] = [];
    if (allowSquash) {
      out.push({ value: "squash", label: "Squash and merge" });
    }
    if (allowMerge) {
      out.push({
        value: "merge",
        label: "Create a merge commit",
      });
    }
    if (allowRebase) {
      out.push({ value: "rebase", label: "Rebase and merge" });
    }
    return out;
  }

  const methods = buildMethods();

  // Props are stable for the lifetime of this modal, so these
  // editable fields intentionally capture their initial values.
  let selectedMethod = $state<Method>(methods[0]?.value ?? "squash");
  const initialCommit = untrack(() => defaultMergeCommit({
    number,
    title: prTitle,
    body: prBody,
    author: prAuthor,
    authorDisplayName: prAuthorDisplayName,
  }));
  let commitTitle = $state(initialCommit.title);
  let commitMessage = $state(initialCommit.message);
  let deleteWorkspaceAfterMerge = $state(true);

  function ciIndicator(status: string): SelectDropdownOption["indicator"] {
    if (ciStatusHasFailed(status)) return { tone: "danger", title: "CI failed" };
    if (ciStatusIsPending(status)) return { tone: "warning", title: "CI running" };
    if (status.toLowerCase() === "success") return { tone: "success", title: "CI passed" };
    return undefined;
  }

  const targetOptions: SelectDropdownOption[] = $derived(stackTargets.map((member) => {
    const indicator = ciIndicator(liveMember(member).ci_status);
    return {
      value: String(member.number),
      label: `#${member.number} ${member.title}`,
      triggerLabel: `#${member.number}`,
      ...(indicator && { indicator }),
    };
  }));

  function chooseTarget(value: string): void {
    const member = stackTargets.find((candidate) => String(candidate.number) === value);
    if (!member || member.number === targetNumber) return;
    targetNumber = member.number;
    const commit = member.number === number
      ? initialCommit
      : { title: `${member.title} (#${member.number})`, message: "" };
    commitTitle = commit.title;
    commitMessage = commit.message;
    bypassRules = false;
  }

  function mergeParams(): MergeParams {
    return {
      commit_title: commitTitle,
      commit_message: commitMessage,
      method: selectedMethod,
      ...(workspaceId && workspaceLands && deleteWorkspaceAfterMerge && { delete_workspace_id: workspaceId }),
      ...(targetPin !== "" && { expected_head_sha: targetPin }),
      ...(sendsBypass ? { bypass_rules: true } : {}),
    };
  }

  function handleMergeProblem(problem: ProblemBody, mergedNumber: number, pin: string): void {
    const reason = isProblem(problem) ? problemConflictReason(problem) : undefined;
    if (reason && reason !== "conflict") {
      onstateconflict?.(
        reason,
        isProblem(problem) ? problemConflictContext(problem) : undefined,
        pin,
        { provider, platformHost, repositoryKey, owner, name, repoPath },
        mergedNumber,
        routeGenerationAtOpen,
      );
    }
  }

  function submitMerge(deferred: boolean): void {
    if (mergeBlocked || (!deferred && !immediateAllowed)) return;
    const params = mergeParams();
    const mergedNumber = targetNumber;
    const pin = targetPin;
    const target = `${repoPath} #${mergedNumber}`;
    detail.mergePull({ provider, platformHost, repositoryKey, owner, name, repoPath }, mergedNumber, params, deferred, {
      onProblem: (problem) => handleMergeProblem(problem, mergedNumber, pin),
      onFailure: (message) => showFlash(`${target}: ${message}`, { tone: "danger" }),
    }, number);
    onclose();
  }

  function handleMerge(): void {
    if (offerDeferredMerge) {
      submitMerge(true);
      return;
    }
    submitMerge(false);
  }

  function handleMergeAnyway(): void {
    submitMerge(false);
  }

  function methodLabel(): string {
    return (
      methods.find(m => m.value === selectedMethod)?.label
      ?? "Merge"
    );
  }

  function primaryButtonLabel(): string {
    if (mergeQueue) {
      return offerDeferredMerge ? "Add to queue after CI" : "Add to merge queue";
    }
    if (offerDeferredMerge) return "Merge after CI";
    if (landingCIFailed) return "Merge Anyway";
    return stackLandingCount ? `${methodLabel()} ${stackLandingCount} pull requests` : methodLabel();
  }

</script>

<Modal
  title={modalTitle()}
  width="min(560px, 92vw)"
  maxWidth="min(560px, 92vw)"
  {onclose}
>
  <div class="merge-body">
      {#if stackTargets.length > 1}
        <div class="field">
          <span class="field-label">Merge up to</span>
          <SelectDropdown
            title="Merge up to"
            value={String(targetNumber)}
            options={targetOptions}
            onchange={chooseTarget}
          />
        </div>
      {/if}
      {#if nativeStackMerge}
        <div class="stack-merge-note">
          {#if stackLandingCount}
            <p>
              {#if mergeQueue}
                Joins the queue as one group: {numberList(nativeStackMerge.landing)}.
              {:else}
                This merges {stackLandingCount} pull requests into
                <code>{nativeStackMerge.baseBranch}</code> as one GitHub stack
                merge: {numberList(nativeStackMerge.landing)}.
              {/if}
            </p>
            {#if !mergeQueue}
              <p>GitHub merges the stack asynchronously. Forge shows the result when GitHub finishes.</p>
            {/if}
          {:else}
            <p>Only this pull request merges.</p>
          {/if}
          {#if nativeStackMerge.remaining.length > 0}
            <p>
              {numberList(nativeStackMerge.remaining)}
              {nativeStackMerge.remaining.length === 1 ? "stays" : "stay"} open and
              GitHub rebases {nativeStackMerge.remaining.length === 1 ? "it" : "them"}
              onto <code>{nativeStackMerge.baseBranch}</code>.
            </p>
          {/if}
        </div>
      {:else if stackNote}
        <p class="stack-note">{stackNote}</p>
      {/if}
      {#if !mergeQueue && methods.length > 1}
        <div class="field">
          <span class="field-label">Merge method</span>
          <SegmentedControl
            ariaLabel="Merge method"
            options={methods}
            value={selectedMethod}
            onchange={(value) => {
              selectedMethod = value as Method;
            }}
          />
        </div>
      {/if}

      {#if !mergeQueue}
        <div class="field">
          <label class="field-label" for="commit-title">
            Commit title
          </label>
          <input
            id="commit-title"
            class="field-input"
            type="text"
            bind:value={commitTitle}
          />
        </div>

        <div class="field">
          <label class="field-label" for="commit-message">
            Commit message
          </label>
          <textarea
            id="commit-message"
            class="field-textarea"
            bind:value={commitMessage}
            rows={8}
          ></textarea>
        </div>
      {/if}

      {#if workspaceLands}
        <Checkbox
          checked={deleteWorkspaceAfterMerge}
          label="Delete workspace after merge"
          onchange={(checked) => {
            deleteWorkspaceAfterMerge = checked;
          }}
        />
      {/if}

      {#if targetDraft}
        <div class="ci-defer-note" role="alert">
          {#if mergingThis}
            This pull request is a draft. Mark it ready for review before merging.
          {:else}
            #{targetNumber} is a draft. Mark it ready for review before merging.
          {/if}
        </div>
      {/if}
      {#if targetImmediateBlocked && targetCanBypass}
        <Checkbox
          checked={bypassRules}
          onchange={(checked) => {
            bypassRules = checked;
          }}
        >
          <span class="bypass-label">
            <ShieldAlertIcon size={14} strokeWidth={2.2} aria-hidden="true" />
            Bypass branch protection and repository rules
          </span>
        </Checkbox>
      {/if}
      {#if landingCIFailed}
        <div class="ci-defer-note" role="alert">
          {#if !targetCIFailed}
            CI failed on {numberList(belowCI.failed)}.
          {:else if mergeQueue}
            CI has failed. The merge queue may remove the pull request if required checks fail.
          {:else}
            CI has failed. Merging now will include changes with failing checks.
          {/if}
        </div>
      {/if}
      {#if targetQueued}
        <div class="ci-defer-note">
          {#if mergeQueue}
            A request is already waiting to add this pull request to the merge
            queue after CI. Adding it here adds it to the queue immediately
            instead.
          {:else}
            A merge is already queued to run after CI. Merging here merges
            immediately instead.
          {/if}
        </div>
      {:else if landingCIPending}
        <!-- On GitHub the deferred merge goes to GitHub auto-merge, which
             follows the branch's required checks, unless it bypasses rules.
             GitHub's API refuses auto-merge for stacked pull requests, so a
             native stack merge waits in Forge's worker. -->
        <div class="ci-defer-note">
          {#if nativeStackMerge}
            CI is still running on {numberList(pendingNumbers)}. Forge
            {mergeQueue ? "adds" : "merges"} {stackLandingCount ? "the stack" : "it"}{mergeQueue ? " to the merge queue" : ""}
            once {stackLandingCount ? "every included pull request passes CI" : "CI passes"}, and stops waiting if it restarts.
          {:else if provider === "github" && !sendsBypass && mergeQueue}
            CI is still running. If GitHub accepts auto-merge for this pull request, GitHub adds it to the merge queue once the branch's required checks and reviews pass; otherwise Forge adds it only if the checks that are pending now pass.
          {:else if provider === "github" && !sendsBypass}
            CI is still running. If GitHub accepts auto-merge for this pull request, GitHub merges it once the branch's required checks and reviews pass; otherwise Forge merges it only if the checks that are pending now pass.
          {:else if mergeQueue}
            CI is still running. This adds the pull request to the merge queue only if the checks that are pending now pass.
          {:else}
            CI is still running. This will merge only if the checks that are pending now pass.
          {/if}
        </div>
      {/if}

    {#if headPinMissing}
      <p class="head-pin-note">
        The reviewed head commit has not been synced yet. Reload after the
        next sync and re-review before merging.
      </p>
    {/if}
  </div>

  {#snippet footer()}
    <!-- The immediate alternative to a deferred merge sits apart on the left,
         so the row spans the body's width and reads as one primary choice. The
         Cancel/primary pair never splits when a narrow panel wraps the row. -->
    <div class="merge-actions">
      {#if offerDeferredMerge && (!targetImmediateBlocked || targetCanBypass)}
        <Button
          class="btn btn--merge-now"
          onclick={handleMergeAnyway}
          disabled={mergeBlocked || !immediateAllowed}
          tone="neutral"
          surface="outline"
        >
          {mergeQueue ? "Add to queue now" : "Merge now"}
        </Button>
      {/if}
      <div class="merge-actions-main">
        <Button
          class="btn btn--secondary"
          onclick={onclose}
          tone="neutral"
          surface="outline"
        >
          Cancel
        </Button>
        <Button
          class="btn btn--primary btn--green"
          onclick={handleMerge}
          disabled={primaryBlocked}
          tone="success"
          surface="solid"
        >
          {primaryButtonLabel()}
        </Button>
      </div>
    </div>
  {/snippet}
</Modal>

<style>
  .stack-note {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.4;
  }

  .stack-merge-note {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 8px 10px;
    border: 1px solid color-mix(in srgb, var(--accent-blue) 40%, transparent);
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1.4;
  }

  .stack-merge-note p {
    margin: 0;
  }

  .stack-merge-note code {
    font-family: var(--font-mono);
  }

  .bypass-label {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--accent-red);
  }

  .ci-defer-note {
    padding: 8px 10px;
    border: 1px solid var(--accent-amber-soft, rgba(217, 119, 6, 0.35));
    border-radius: var(--radius-sm);
    background: var(--accent-amber-soft, rgba(217, 119, 6, 0.12));
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.35;
  }

  .head-pin-note {
    margin: 0 0 var(--space-3, 12px);
    color: var(--text-secondary, #888);
    font-size: var(--font-size-sm);
  }

  .merge-actions {
    display: flex;
    flex: 1;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }

  .merge-actions-main {
    display: flex;
    gap: 8px;
    margin-inline-start: auto;
  }

  .merge-body {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .field-label {
    font-size: var(--font-size-sm);
    font-weight: 500;
    color: var(--text-secondary);
  }

  .field-input {
    font-size: var(--font-size-root);
    padding: 6px 10px;
    background: var(--bg-inset);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
  }
  .field-input:focus {
    border-color: var(--accent-blue);
    outline: none;
  }

  .field-textarea {
    font-size: var(--font-size-root);
    padding: 8px 10px;
    background: var(--bg-inset);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    resize: vertical;
    line-height: 1.5;
    font-family: var(--font-mono);
    max-height: 300px;
  }
  .field-textarea:focus {
    border-color: var(--accent-blue);
    outline: none;
  }

</style>
