<script lang="ts">
  import { Button, Checkbox, Modal } from "@kenn-io/kit-ui";
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
  import type { NativeStackMergeScope } from "./stack-merge.js";
  import type { RepositoryKey } from "../../api/repository-key.js";
  import { defaultMergeCommit } from "./merge-policy.js";

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
     * Set when the provider owns the stack: the merge also lands every
     * unmerged pull request below this one, so the modal names them all.
     */
    nativeStackMerge?: NativeStackMergeScope | undefined;
    /**
     * Set when the base branch uses a merge queue: submitting adds the
     * pull request to the queue, which merges it with its own configured
     * method, so method and commit text do not apply. A requested workspace
     * deletion waits for the queue's merge.
     */
    mergeQueue?: { branch: string; mergeMethod: string } | undefined;
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
    alreadyQueued = false, workspaceId, stackNote, nativeStackMerge, mergeQueue,
    onclose, onstateconflict,
  }: Props = $props();

  // Offer to queue a deferred merge only when none is queued yet.
  const offerDeferredMerge = $derived(deferUntilChecksPass && !alreadyQueued);

  const queueMethodLabels: Record<string, string> = {
    merge: "a merge commit",
    squash: "squash and merge",
    rebase: "rebase and merge",
  };

  // The queue's merge method is omitted while Forge has not read it yet.
  const queueMethodPhrase = $derived(
    mergeQueue?.mergeMethod
      ? ` using ${queueMethodLabels[mergeQueue.mergeMethod] ?? mergeQueue.mergeMethod}`
      : "",
  );

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
    if (mergeQueue) return stackLandingCount ? "Add Stack to Merge Queue" : "Add to Merge Queue";
    return stackLandingCount ? "Merge Stack" : "Merge Pull Request";
  }

  // Captured once when the modal opens: a background detail refresh
  // must not silently rebind the pin to a head the user has not seen
  // while the form is already on screen. If the head really moved, the
  // server rejects this stale pin and the conflict flow takes over.
  const pinnedHeadShaAtOpen = untrack(() => (expectedHeadSha ?? "").trim());
  const routeGenerationAtOpen = untrack(() => routeGeneration);

  // A head-binding provider cannot merge without a pinned head; the user
  // must wait for sync and re-review before merging.
  const headPinMissing = untrack(() => requireHeadPin) && pinnedHeadShaAtOpen === "";

  const mergeBlocked = $derived(headPinMissing || isDraft);

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

  function mergeParams(): MergeParams {
    return {
      commit_title: commitTitle,
      commit_message: commitMessage,
      method: selectedMethod,
      ...(workspaceId && deleteWorkspaceAfterMerge && { delete_workspace_id: workspaceId }),
      ...(pinnedHeadShaAtOpen !== "" && { expected_head_sha: pinnedHeadShaAtOpen }),
    };
  }

  function handleMergeProblem(problem: ProblemBody): void {
    const reason = isProblem(problem) ? problemConflictReason(problem) : undefined;
    if (reason && reason !== "conflict") {
      onstateconflict?.(
        reason,
        isProblem(problem) ? problemConflictContext(problem) : undefined,
        pinnedHeadShaAtOpen,
        { provider, platformHost, repositoryKey, owner, name, repoPath },
        number,
        routeGenerationAtOpen,
      );
    }
  }

  function submitMerge(deferred: boolean): void {
    if (mergeBlocked) return;
    const params = mergeParams();
    const target = `${repoPath} #${number}`;
    detail.mergePull({ provider, platformHost, repositoryKey, owner, name, repoPath }, number, params, deferred, {
      onProblem: handleMergeProblem,
      onFailure: (message) => showFlash(`${target}: ${message}`, { tone: "danger" }),
    });
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
      return offerDeferredMerge ? "Add to merge queue after CI is complete" : "Add to merge queue";
    }
    if (offerDeferredMerge) return "Merge after CI is complete";
    if (ciFailed) return "Merge Anyway";
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
      {#if nativeStackMerge}
        <div class="stack-merge-note">
          {#if stackLandingCount}
            <p>
              {#if mergeQueue}
                This adds {stackLandingCount} pull requests to the merge queue as one
                group: {numberList(nativeStackMerge.landing)}.
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
      {#if mergeQueue}
        <p class="merge-queue-note">
          <code>{mergeQueue.branch}</code> uses a merge queue. This adds the pull request
          to the queue, which merges it{queueMethodPhrase} once its checks pass.
        </p>
      {:else if methods.length > 1}
        <div class="field" role="group" aria-label="Merge method">
          <span class="field-label">Merge method</span>
          <div class="method-options">
            {#each methods as m (m.value)}
              <label
                class="method-option"
                class:method-option--active={selectedMethod === m.value}
              >
                <input
                  type="radio"
                  name="merge-method"
                  value={m.value}
                  bind:group={selectedMethod}
                />
                {m.label}
              </label>
            {/each}
          </div>
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

      {#if workspaceId}
        <Checkbox
          checked={deleteWorkspaceAfterMerge}
          label="Delete workspace after merge"
          onchange={(checked) => {
            deleteWorkspaceAfterMerge = checked;
          }}
        />
      {/if}

      {#if isDraft}
        <div class="ci-defer-note" role="alert">
          This pull request is a draft. Mark it ready for review before merging.
        </div>
      {/if}
      {#if ciFailed}
        <div class="ci-defer-note" role="alert">
          {#if mergeQueue}
            CI has failed. The merge queue may remove the pull request if required checks fail.
          {:else}
            CI has failed. Merging now will include changes with failing checks.
          {/if}
        </div>
      {/if}
      {#if alreadyQueued}
        <div class="ci-defer-note">
          {#if mergeQueue}
            A request is already waiting to add this pull request to the merge
            queue if the CI checks that were pending when it was made pass.
            Adding it here adds it to the queue immediately instead.
          {:else}
            A merge is already queued; it runs only if the CI checks that were
            pending when it was queued pass. Merging here merges immediately
            instead.
          {/if}
        </div>
      {:else if deferUntilChecksPass}
        <div class="ci-defer-note">
          {#if mergeQueue}
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
      disabled={mergeBlocked}
      tone="success"
      surface="solid"
    >
      {primaryButtonLabel()}
    </Button>
    {#if offerDeferredMerge}
      <Button
        class="btn btn--merge-anyway"
        onclick={handleMergeAnyway}
        disabled={mergeBlocked}
        tone="success"
        surface="soft"
      >
        {mergeQueue ? "Add to queue now" : "Merge Anyway"}
      </Button>
    {/if}
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

  .ci-defer-note {
    padding: 8px 10px;
    border: 1px solid var(--accent-amber-soft, rgba(217, 119, 6, 0.35));
    border-radius: var(--radius-sm);
    background: var(--accent-amber-soft, rgba(217, 119, 6, 0.12));
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.35;
  }

  .merge-queue-note {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.4;
  }

  .merge-queue-note code {
    font-family: var(--font-mono);
    color: var(--text-primary);
  }

  .head-pin-note {
    margin: 0 0 var(--space-3, 12px);
    color: var(--text-secondary, #888);
    font-size: var(--font-size-sm);
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

  .method-options {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }

  .method-option {
    font-size: var(--font-size-sm);
    font-weight: 500;
    padding: 5px 12px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-inset);
    color: var(--text-secondary);
    cursor: pointer;
    transition: all 0.1s;
  }
  .method-option input { display: none; }
  .method-option:hover {
    border-color: var(--accent-blue);
    color: var(--text-primary);
  }
  .method-option--active {
    background: color-mix(
      in srgb, var(--accent-blue) 12%, transparent
    );
    border-color: var(--accent-blue);
    color: var(--accent-blue);
  }

</style>
