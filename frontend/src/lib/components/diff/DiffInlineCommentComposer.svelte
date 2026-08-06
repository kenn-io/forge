<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import SendIcon from "@lucide/svelte/icons/send";
  import XIcon from "@lucide/svelte/icons/x";
  import { tick } from "svelte";
  import type { AppExecution, AppRuntime } from "../../app/runtime.js";
  import { nextAnimationFrame } from "../../browser/animation-frame.js";
  import type { DiffReviewLineRange } from "../../stores/diff-review-draft.svelte.js";
  import { getStores } from "../../context.js";

  interface Props {
    runtime: AppRuntime;
    range: DiffReviewLineRange;
    onclose?: (() => void) | undefined;
  }

  const { runtime, range, onclose }: Props = $props();
  const { diffReviewDraft } = getStores();

  let body = $state("");
  let textareaEl: HTMLTextAreaElement | undefined = $state();
  let focusExecution: AppExecution<void, never> | undefined;
  let autosizeExecution: AppExecution<void, never> | undefined;
  const submitting = $derived(diffReviewDraft.isSubmitting());
  const error = $derived(diffReviewDraft.getError());

  function setupTextarea(node: HTMLTextAreaElement) {
    textareaEl = node;
    focusExecution = runtime.runCommand(
      Effect.promise(() => tick()).pipe(
        Effect.andThen(Effect.sync(() => {
          // Focus exactly once. Retrying focus across frames/timers makes any
          // visible focus treatment flicker as the indicator blinks per retry.
          if (node.isConnected && !node.disabled) node.focus({ preventScroll: true });
          scheduleAutosizeTextarea();
        })),
      ),
      { operation: "focus inline diff comment", safeContext: {}, onFailure: () => {} },
    );

    return {
      destroy(): void {
        focusExecution?.interrupt();
        autosizeExecution?.interrupt();
        if (textareaEl === node) textareaEl = undefined;
      },
    };
  }

  function autosizeTextarea(): void {
    if (!textareaEl) return;
    const style = getComputedStyle(textareaEl);
    const borderHeight = Number.parseFloat(style.borderTopWidth) +
      Number.parseFloat(style.borderBottomWidth);
    textareaEl.style.height = "auto";
    textareaEl.style.height = `${textareaEl.scrollHeight + borderHeight}px`;
  }

  function scheduleAutosizeTextarea(): void {
    autosizeTextarea();
    autosizeExecution?.interrupt();
    autosizeExecution = runtime.runCommand(
      nextAnimationFrame.pipe(Effect.andThen(Effect.sync(autosizeTextarea)), Effect.asVoid),
      { operation: "autosize inline diff comment", safeContext: {}, onFailure: () => {} },
    );
  }

  function submit(): void {
    const nextBody = body.trim();
    if (!nextBody) return;
    diffReviewDraft.createComment(nextBody, range, {
      onSuccess: () => {
        body = "";
        onclose?.();
      },
    });
  }

</script>

<div class="inline-composer">
  <textarea
    use:setupTextarea
    bind:this={textareaEl}
    bind:value={body}
    placeholder="Leave a comment"
    disabled={submitting}
    rows="3"
    oninput={scheduleAutosizeTextarea}
  ></textarea>
  {#if error}
    <p class="composer-error">{error}</p>
  {/if}
  <div class="composer-actions">
    <Button
      class="composer-btn"
      size="sm"
      onclick={onclose}
      disabled={submitting}
    >
      <XIcon size={14} />
      Cancel
    </Button>
    <Button
      class="composer-btn composer-btn--primary"
      tone="info"
      surface="solid"
      size="sm"
      onclick={submit}
      disabled={submitting || body.trim() === ""}
    >
      <SendIcon size={14} />
      {submitting ? "Saving..." : "Add comment"}
    </Button>
  </div>
</div>

<style>
  .inline-composer {
    box-sizing: border-box;
    margin: 6px 12px 8px;
    padding: 8px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-surface);
    width: calc(100% - 24px);
    max-width: calc(100% - 24px);
    min-width: 0;
    overflow: hidden;
  }

  @container (max-width: 520px) {
    .inline-composer {
      margin: 6px 8px 8px;
      width: calc(100% - 16px);
      max-width: calc(100% - 16px);
    }
  }

  textarea {
    box-sizing: border-box;
    width: 100%;
    max-width: 100%;
    min-height: 72px;
    max-height: 75vh;
    resize: none;
    overflow-y: auto;
    padding: 8px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-inset);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-md);
  }

  textarea:focus {
    border-color: var(--border-muted);
    outline: none;
  }

  textarea:focus-visible {
    border-color: var(--accent-blue);
    outline: none;
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 42%, transparent);
  }

  .composer-error {
    margin-top: 6px;
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .composer-actions {
    display: flex;
    justify-content: flex-end;
    flex-wrap: wrap;
    gap: 6px;
    margin-top: 8px;
  }

  :global(.composer-btn.kit-button) {
    min-height: 28px;
  }

  :global(.composer-btn--primary.kit-button) {
    border-color: var(--accent-blue);
    background: var(--accent-blue);
    color: var(--text-on-accent);
  }
</style>
