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
  import {
    beginWorkspaceDeletion,
    endWorkspaceDeletion,
    markWorkspaceIdDeleted,
  } from "../../stores/workspace-create-pending.svelte.js";

  const { detail } = getStores();

  onMount(() => pushModalFrame("merge-modal", []));

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
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
    /**
     * True while a background merge is already queued for this PR. The
     * deferred action is withheld (the server would 409 on a second
     * queue) and the modal offers an immediate merge instead.
     */
    alreadyQueued?: boolean;
    /** Exact workspace to delete after a successful merge. */
    workspaceId?: string | undefined;
    /** Warning shown when the configured override permits a mid-stack merge. */
    midStackWarning?: string | undefined;
    onclose: () => void;
    onmerged: (cleanupWarning?: string, deletedWorkspaceId?: string) => void;
    /** Called when a deferred merge was accepted and now waits on CI. */
    onqueued: () => void;
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
    owner, name, number, provider, platformHost, repoPath, prTitle, prBody,
    prAuthor, prAuthorDisplayName,
    allowSquash, allowMerge, allowRebase,
    expectedHeadSha, requireHeadPin = false, routeGeneration = 0,
    deferUntilChecksPass = false,
    alreadyQueued = false, workspaceId, midStackWarning,
    onclose, onmerged, onqueued, onstateconflict,
  }: Props = $props();

  // Offer to queue a deferred merge only when none is queued yet.
  const offerDeferredMerge = $derived(deferUntilChecksPass && !alreadyQueued);

  // Captured once when the modal opens: a background detail refresh
  // must not silently rebind the pin to a head the user has not seen
  // while the form is already on screen. If the head really moved, the
  // server rejects this stale pin and the conflict flow takes over.
  const pinnedHeadShaAtOpen = untrack(() => (expectedHeadSha ?? "").trim());
  const routeGenerationAtOpen = untrack(() => routeGeneration);

  // A head-binding provider cannot merge without a pinned head; the user
  // must wait for sync and re-review before merging.
  const headPinMissing = untrack(() => requireHeadPin) && pinnedHeadShaAtOpen === "";

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

  function initialCommitTitle(): string {
    return `${prTitle} (#${number})`;
  }

  function initialCoAuthor(): string {
    const coAuthorName = prAuthorDisplayName || prAuthor;
    return `Co-authored-by: ${coAuthorName} <${prAuthor}@users.noreply.github.com>`;
  }

  function initialCommitMessage(): string {
    const coAuthor = initialCoAuthor();
    return prBody ? `${prBody}\n\n${coAuthor}` : coAuthor;
  }

  // Props are stable for the lifetime of this modal, so these
  // editable fields intentionally capture their initial values.
  let selectedMethod = $state<Method>(methods[0]?.value ?? "squash");
  let commitTitle = $state(initialCommitTitle());
  let commitMessage = $state(initialCommitMessage());
  let deleteWorkspaceAfterMerge = $state(true);

  let activeMergeSubmission = $state<"deferred" | "immediate" | null>(null);
  let error = $state<string | null>(null);
  const merging = $derived(activeMergeSubmission !== null);

  function mergeParams(): MergeParams {
    return {
      commit_title: commitTitle,
      commit_message: commitMessage,
      method: selectedMethod,
      ...(workspaceId && deleteWorkspaceAfterMerge && { delete_workspace_id: workspaceId }),
      ...(pinnedHeadShaAtOpen !== "" && { expected_head_sha: pinnedHeadShaAtOpen }),
    };
  }

  function handleMergeProblem(problem: ProblemBody): boolean {
    const reason = isProblem(problem) ? problemConflictReason(problem) : undefined;
    if (reason && reason !== "conflict") {
      onstateconflict?.(
        reason,
        isProblem(problem) ? problemConflictContext(problem) : undefined,
        pinnedHeadShaAtOpen,
        { provider, platformHost, owner, name, repoPath },
        number,
        routeGenerationAtOpen,
      );
      onclose();
      return true;
    }
    const message = problem.detail ?? problem.title ?? "failed to merge pull request";
    if (reason === "conflict") {
      error = message;
      return true;
    }
    return false;
  }

  function submitMerge(deferred: boolean): void {
    if (headPinMissing) return;
    activeMergeSubmission = deferred ? "deferred" : "immediate";
    error = null;
    let problemHandled = false;
    const params = mergeParams();
    const cleanupWorkspaceId = deferred ? undefined : params.delete_workspace_id;
    if (cleanupWorkspaceId) beginWorkspaceDeletion(cleanupWorkspaceId, undefined);
    detail.mergePull({ provider, platformHost, owner, name, repoPath }, number, params, deferred, {
      onProblem: (problem) => {
        problemHandled = handleMergeProblem(problem);
      },
      onFailure: (message) => {
        if (!problemHandled) showFlash(message, { tone: "danger" });
      },
      onSuccess: (outcome) => {
        if (deferred) {
          onqueued();
          return;
        }
        const deletedWorkspaceId = cleanupWorkspaceId && outcome.cleanupWarning === undefined
          ? cleanupWorkspaceId
          : undefined;
        if (deletedWorkspaceId) markWorkspaceIdDeleted(deletedWorkspaceId);
        onmerged(outcome.cleanupWarning, deletedWorkspaceId);
      },
      onSettled: () => {
        if (cleanupWorkspaceId) endWorkspaceDeletion(cleanupWorkspaceId, undefined);
        activeMergeSubmission = null;
      },
    });
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
    if (activeMergeSubmission === "deferred") return "Merge scheduled...";
    if (activeMergeSubmission === "immediate" && !offerDeferredMerge) return "Merging...";
    return offerDeferredMerge ? "Merge after CI is complete" : methodLabel();
  }

  function mergeAnywayButtonLabel(): string {
    return activeMergeSubmission === "immediate" ? "Merging..." : "Merge Anyway";
  }
</script>

<Modal
  title="Merge Pull Request"
  width="min(560px, 92vw)"
  maxWidth="min(560px, 92vw)"
  {onclose}
>
  <div class="merge-body">
      {#if midStackWarning}
        <div class="mid-stack-warning" role="alert">
          <strong>Warning: this is a mid-stack merge.</strong>
          <span>{midStackWarning} Merging now can leave the remaining stack based on an unexpected branch.</span>
        </div>
      {/if}
      {#if methods.length > 1}
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

      {#if workspaceId}
        <Checkbox
          checked={deleteWorkspaceAfterMerge}
          label="Delete workspace after merge"
          onchange={(checked) => {
            deleteWorkspaceAfterMerge = checked;
          }}
        />
      {/if}

      {#if error}
        <p class="merge-error">{error}</p>
      {/if}
      {#if alreadyQueued}
        <div class="ci-defer-note">
          A merge is already queued; it runs only if the CI checks that were
          pending when it was queued pass. Merging here merges immediately
          instead.
        </div>
      {:else if deferUntilChecksPass}
        <div class="ci-defer-note">
          CI is still running. This will merge only if the checks that are pending now pass.
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
      disabled={merging}
      tone="neutral"
      surface="outline"
    >
      Cancel
    </Button>
    <Button
      class="btn btn--primary btn--green"
      onclick={handleMerge}
      disabled={merging || headPinMissing}
      tone="success"
      surface="solid"
    >
      {primaryButtonLabel()}
    </Button>
    {#if offerDeferredMerge}
      <Button
        class="btn btn--merge-anyway"
        onclick={handleMergeAnyway}
        disabled={merging || headPinMissing}
        tone="success"
        surface="soft"
      >
        {mergeAnywayButtonLabel()}
      </Button>
    {/if}
  {/snippet}
</Modal>

<style>
  .mid-stack-warning {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 10px 12px;
    border: 1px solid var(--accent-amber-soft, rgba(217, 119, 6, 0.45));
    border-radius: var(--radius-sm);
    background: var(--accent-amber-soft, rgba(217, 119, 6, 0.12));
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.4;
  }

  .mid-stack-warning strong {
    color: var(--text-primary);
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

  .merge-error {
    font-size: var(--font-size-sm);
    color: var(--accent-red);
    padding: 8px 10px;
    background: color-mix(
      in srgb, var(--accent-red) 8%, transparent
    );
    border-radius: var(--radius-sm);
  }

</style>
