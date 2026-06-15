<script lang="ts">
  import { onMount, untrack } from "svelte";

  import { isProblem, problemConflictContext, problemConflictReason } from "../../api/problems.js";
  import { providerItemPath, providerRouteParams } from "../../api/provider-routes.js";
  import { getClient } from "../../context.js";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";
  import { bucketForCheck, parseCIChecks } from "../../utils/ci-buckets.js";
  import type { CICheck } from "../../api/types.js";
  import ActionButton from "../shared/ActionButton.svelte";

  const DEFAULT_CI_POLL_INTERVAL_MS = 60_000;

  const client = getClient();

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
    /** When true, the primary action waits for currently pending CI before merging. */
    deferUntilChecksPass?: boolean;
    /** Current provider CI snapshot for the loaded pull request. */
    checksJSON?: string;
    /** Refreshes the loaded CI snapshot while a deferred merge is armed. */
    onrefreshci?: (() => Promise<void>) | undefined;
    /** Test hook for shortening the default one-minute polling interval. */
    ciPollIntervalMs?: number;
    onclose: () => void;
    onmerged: () => void;
    onheadconflict?: ((reason: "stale_state" | "head_unknown", context?: string) => void) | undefined;
  }

  const {
    owner, name, number, provider, platformHost, repoPath, prTitle, prBody,
    prAuthor, prAuthorDisplayName,
    allowSquash, allowMerge, allowRebase,
    expectedHeadSha, requireHeadPin = false,
    deferUntilChecksPass = false,
    checksJSON = "",
    onrefreshci,
    ciPollIntervalMs = DEFAULT_CI_POLL_INTERVAL_MS,
    onclose, onmerged, onheadconflict,
  }: Props = $props();

  // Captured once when the modal opens: a background detail refresh
  // must not silently rebind the pin to a head the user has not seen
  // while the form is already on screen. If the head really moved, the
  // server rejects this stale pin and the conflict flow takes over.
  const pinnedHeadShaAtOpen = untrack(() => (expectedHeadSha ?? "").trim());

  // A head-binding provider cannot merge without a pinned head; the user
  // must wait for sync and re-review before merging.
  const headPinMissing = untrack(() => requireHeadPin) && pinnedHeadShaAtOpen === "";

  type Method = "merge" | "squash" | "rebase";
  type MethodOption = { value: Method; label: string };
  type MergeParams = {
    commit_title: string;
    commit_message: string;
    method: Method;
    expected_head_sha?: string;
  };

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

  let merging = $state(false);
  let error = $state<string | null>(null);
  let waitingForCI = $state(false);
  let deferredPendingCheckKeys = $state<string[] | null>(null);
  let deferredMergeStarted = $state(false);
  let deferredCIError = $state<string | null>(null);

  const parsedChecks = $derived(parseCIChecks(checksJSON));
  const deferredCIState = $derived.by(() =>
    deferredPendingCheckKeys === null
      ? "idle"
      : pendingCheckState(deferredPendingCheckKeys, parsedChecks.checks),
  );
  const pendingAtConfirmationCount = $derived(deferredPendingCheckKeys?.length ?? 0);

  function checkKey(check: CICheck): string {
    return `${check.app}\0${check.name}`;
  }

  function pendingCheckKeys(checks: CICheck[]): string[] {
    return checks
      .filter((check) => bucketForCheck(check) === "pending")
      .map(checkKey);
  }

  function pendingCheckState(keys: readonly string[], checks: readonly CICheck[]): "pending" | "passed" | "failed" {
    if (keys.length === 0) return "passed";
    const checksByKey = new Map(checks.map((check) => [checkKey(check), check]));
    let hasPending = false;
    for (const key of keys) {
      const check = checksByKey.get(key);
      if (check === undefined) {
        hasPending = true;
        continue;
      }
      switch (bucketForCheck(check)) {
        case "passed":
        case "skipped":
          break;
        case "failed":
        case "unknown":
          return "failed";
        case "pending":
          hasPending = true;
          break;
      }
    }
    return hasPending ? "pending" : "passed";
  }

  async function submitMerge(): Promise<void> {
    if (headPinMissing) return;
    merging = true;
    error = null;
    try {
      // Pin the merge to the head the user reviewed; the server rejects
      // the request when the synced head has moved past it.
      const params: MergeParams = {
        commit_title: commitTitle,
        commit_message: commitMessage,
        method: selectedMethod,
        ...(pinnedHeadShaAtOpen !== "" && { expected_head_sha: pinnedHeadShaAtOpen }),
      };
      const ref = { provider, platformHost, owner, name, repoPath };
      const { error } = await client.POST(providerItemPath("pulls", ref, "/merge"), {
        params: { path: { ...providerRouteParams(ref), number } },
        body: params,
      });
      if (error) {
        // Head-pinning conflicts close the modal: the user must
        // re-review the refreshed detail before retrying, so an
        // inline retry from this stale form would be wrong.
        const reason = isProblem(error) ? problemConflictReason(error) : undefined;
        if (reason === "stale_state" || reason === "head_unknown") {
          onheadconflict?.(reason, isProblem(error) ? problemConflictContext(error) : undefined);
          onclose();
          return;
        }
        throw new Error(error.detail ?? error.title ?? "failed to merge pull request");
      }
      onmerged();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      merging = false;
    }
  }

  function handleMerge(): void {
    if (deferUntilChecksPass && !waitingForCI) {
      const keys = pendingCheckKeys(parsedChecks.checks);
      if (keys.length > 0) {
        deferredPendingCheckKeys = keys;
        waitingForCI = true;
        deferredMergeStarted = false;
        deferredCIError = null;
        error = null;
        return;
      }
    }
    void submitMerge();
  }

  $effect(() => {
    if (!waitingForCI || deferredPendingCheckKeys === null) return;
    if (deferredCIState === "failed") {
      waitingForCI = false;
      deferredCIError = "A check that was pending failed; merge was not performed.";
      return;
    }
    if (deferredCIState === "passed") {
      if (deferredMergeStarted) return;
      deferredMergeStarted = true;
      void submitMerge();
      return;
    }
    if (onrefreshci === undefined) return;
    const refresh = () => {
      void onrefreshci().catch((err) => {
        deferredCIError = err instanceof Error ? err.message : String(err);
      });
    };
    const interval = setInterval(refresh, ciPollIntervalMs);
    return () => clearInterval(interval);
  });

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === "Escape") {
      e.preventDefault();
      onclose();
    }
  }

  function methodLabel(): string {
    return (
      methods.find(m => m.value === selectedMethod)?.label
      ?? "Merge"
    );
  }

  function deferredStatusText(): string {
    const suffix = pendingAtConfirmationCount === 1 ? "check" : "checks";
    return `Waiting for ${pendingAtConfirmationCount} pending ${suffix} to pass.`;
  }

  function primaryButtonLabel(): string {
    if (merging) return "Merging...";
    if (waitingForCI) return "Waiting for CI...";
    return deferUntilChecksPass ? "Merge after CI is complete" : methodLabel();
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="modal-overlay"
  onclick={onclose}
  onkeydown={handleKeydown}
>
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="modal" onclick={(e) => e.stopPropagation()}>
    <div class="modal-header">
      <h3 class="modal-title">Merge Pull Request</h3>
      <button
        class="modal-close"
        onclick={onclose}
        title="Cancel (Esc)"
      >
        <svg
          width="16"
          height="16"
          viewBox="0 0 16 16"
          fill="currentColor"
        >
          <path d="M3.72 3.72a.75.75 0 011.06 0L8 6.94l3.22-3.22a.75.75 0 111.06 1.06L9.06 8l3.22 3.22a.75.75 0 11-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 01-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 010-1.06z"/>
        </svg>
      </button>
    </div>

    <div class="modal-body">
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

      {#if error}
        <p class="merge-error">{error}</p>
      {/if}
      {#if deferUntilChecksPass}
        <div class="ci-defer-note" role={waitingForCI ? "status" : undefined}>
          {#if waitingForCI}
            {deferredStatusText()}
          {:else}
            CI is still running. This will merge only if the checks that are pending now pass.
          {/if}
        </div>
      {/if}
      {#if deferredCIError}
        <p class="merge-error">{deferredCIError}</p>
      {/if}
    </div>

    {#if headPinMissing}
      <p class="head-pin-note">
        The reviewed head commit has not been synced yet. Reload after the
        next sync and re-review before merging.
      </p>
    {/if}

    <div class="modal-footer">
      <ActionButton
        class="btn btn--secondary"
        onclick={onclose}
        disabled={merging}
        tone="neutral"
        surface="outline"
      >
        Cancel
      </ActionButton>
      <ActionButton
        class="btn btn--primary btn--green"
        onclick={handleMerge}
        disabled={merging || headPinMissing || waitingForCI}
        tone="success"
        surface="solid"
      >
        {primaryButtonLabel()}
      </ActionButton>
    </div>
  </div>
</div>

<style>
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

  .modal-overlay {
    position: fixed;
    inset: 0;
    background: var(--overlay-bg);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 50;
    animation: fade-in 0.12s ease-out;
  }

  @keyframes fade-in {
    from { opacity: 0; }
    to { opacity: 1; }
  }

  .modal {
    width: min(560px, 92vw);
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-lg);
    display: flex;
    flex-direction: column;
    animation: scale-in 0.12s ease-out;
  }

  @keyframes scale-in {
    from { opacity: 0; transform: scale(0.96); }
    to { opacity: 1; transform: scale(1); }
  }

  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border-muted);
  }

  .modal-title {
    font-size: var(--font-size-lg);
    font-weight: 600;
  }

  .modal-close {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    transition: background 0.1s, color 0.1s;
  }
  .modal-close:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .modal-body {
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    max-height: 60vh;
    overflow-y: auto;
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

  .modal-footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 12px 16px;
    border-top: 1px solid var(--border-muted);
  }
</style>
