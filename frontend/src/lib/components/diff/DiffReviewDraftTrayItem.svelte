<script lang="ts">
  import { Card, IconButton } from "@kenn-io/kit-ui";
  import { onDestroy, onMount, tick } from "svelte";
  import CheckIcon from "@lucide/svelte/icons/check";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import XIcon from "@lucide/svelte/icons/x";
  import type {
    DiffReviewDraftComment,
    DiffReviewDraftCommentEditState,
  } from "../../stores/diff-review-draft.svelte.js";

  interface Props {
    comment: DiffReviewDraftComment;
    location: string;
    disabled: boolean;
    onjump?: ((comment: DiffReviewDraftComment) => void) | undefined;
    ondelete: (id: string) => void;
    onsave: (comment: DiffReviewDraftComment, body: string) => Promise<boolean> | boolean;
    oneditstatechange: (id: string, state: DiffReviewDraftCommentEditState) => void;
  }

  const { comment, location, disabled, onjump, ondelete, onsave, oneditstatechange }: Props = $props();

  let expanded = $state(false);
  let editing = $state(false);
  let draftBody = $state("");
  let saving = $state(false);
  let truncated = $state(false);
  let bodyElement: HTMLParagraphElement | undefined = $state();
  let editorElement: HTMLTextAreaElement | undefined = $state();
  let measureFrame: number | undefined;
  const editStateID = $derived(`tray:${comment.id}`);
  const editDisabled = $derived(disabled || saving);
  const saveDisabled = $derived(editDisabled || draftBody.trim() === "");

  function measureTruncation(): void {
    if (!bodyElement) {
      truncated = false;
      return;
    }
    truncated = bodyElement.scrollHeight > bodyElement.clientHeight + 1
      || bodyElement.scrollWidth > bodyElement.clientWidth + 1;
  }

  function queueMeasure(): void {
    if (measureFrame !== undefined) {
      cancelAnimationFrame(measureFrame);
    }
    measureFrame = requestAnimationFrame(() => {
      measureFrame = undefined;
      measureTruncation();
    });
  }

  function toggleExpanded(): void {
    expanded = !expanded;
    void tick().then(queueMeasure);
  }

  function draftDirty(body: string): boolean {
    return body.trim() !== comment.body;
  }

  function reportEditState(active: boolean, body = draftBody): void {
    oneditstatechange(editStateID, {
      active,
      dirty: active && draftDirty(body),
    });
  }

  function beginEdit(): void {
    draftBody = comment.body;
    editing = true;
    reportEditState(true);
    expanded = true;
    void tick().then(() => editorElement?.focus());
  }

  function cancelEdit(): void {
    draftBody = comment.body;
    editing = false;
    reportEditState(false);
  }

  function handleDraftBodyInput(event: Event): void {
    draftBody = (event.currentTarget as HTMLTextAreaElement).value;
    reportEditState(true);
  }

  async function saveEdit(): Promise<void> {
    const nextBody = draftBody.trim();
    if (!nextBody || saveDisabled) return;
    if (nextBody === comment.body) {
      editing = false;
      reportEditState(false);
      return;
    }
    saving = true;
    try {
      const ok = await onsave(comment, nextBody);
      if (ok) {
        editing = false;
        reportEditState(false);
      }
    } finally {
      saving = false;
    }
  }

  function scheduleMeasure(_body: string, _expanded: boolean): void {
    void tick().then(queueMeasure);
  }

  onMount(() => {
    const observer = typeof ResizeObserver === "undefined"
      ? undefined
      : new ResizeObserver(queueMeasure);
    if (bodyElement) observer?.observe(bodyElement);
    queueMeasure();

    return () => {
      observer?.disconnect();
      if (measureFrame !== undefined) {
        cancelAnimationFrame(measureFrame);
      }
    };
  });

  onDestroy(() => {
    reportEditState(false);
  });

  $effect(() => scheduleMeasure(comment.body, expanded));
</script>

<Card level="default" padding="sm" class="draft-item">
  <div class="draft-item__layout">
    <div class="draft-content">
      <button
        class="draft-jump"
        type="button"
        onclick={() => onjump?.(comment)}
      >
        {location}
      </button>
      {#if editing}
        <textarea
          bind:this={editorElement}
          value={draftBody}
          class="draft-editor"
          aria-label="Draft comment body"
          rows="3"
          disabled={editDisabled}
          oninput={handleDraftBodyInput}
        ></textarea>
      {:else}
        <p
          bind:this={bodyElement}
          class={["draft-body", expanded && "draft-body--expanded"]}
        >
          {comment.body}
        </p>
        {#if truncated || expanded}
          <button class="draft-expand" type="button" onclick={toggleExpanded}>
            {expanded ? "Show less" : "Show full comment"}
          </button>
        {/if}
      {/if}
    </div>
    <div class="draft-actions">
      {#if editing}
        <IconButton
          size="sm"
          tone="success"
          ariaLabel="Save draft comment"
          onclick={() => void saveEdit()}
          disabled={saveDisabled}
        >
          <CheckIcon size={13} />
        </IconButton>
        <IconButton
          size="sm"
          ariaLabel="Cancel editing draft comment"
          onclick={cancelEdit}
          disabled={editDisabled}
        >
          <XIcon size={13} />
        </IconButton>
      {:else}
        <IconButton
          size="sm"
          ariaLabel="Edit draft comment"
          onclick={beginEdit}
          disabled={disabled}
        >
          <PencilIcon size={13} />
        </IconButton>
        <IconButton
          size="sm"
          tone="danger"
          ariaLabel="Delete draft comment"
          onclick={() => ondelete(comment.id)}
          disabled={disabled}
        >
          <XIcon size={13} />
        </IconButton>
      {/if}
    </div>
  </div>
</Card>

<style>
  :global(.draft-item) {
    min-width: 0;
  }

  .draft-item__layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: start;
    gap: var(--space-4);
    min-width: 0;
  }

  .draft-content {
    display: grid;
    gap: var(--space-1);
    min-width: 0;
  }

  .draft-body {
    display: -webkit-box;
    margin: 0;
    overflow: hidden;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1.42;
    overflow-wrap: anywhere;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
  }

  .draft-body--expanded {
    display: block;
    line-clamp: unset;
    -webkit-line-clamp: unset;
  }

  .draft-editor {
    box-sizing: border-box;
    width: 100%;
    min-height: 78px;
    resize: vertical;
    padding: 7px 8px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    line-height: 1.42;
  }

  .draft-jump {
    display: block;
    max-width: 100%;
    padding: 0;
    border: 0;
    overflow: hidden;
    background: transparent;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    line-height: 1.35;
    text-align: left;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;
  }

  .draft-jump:hover {
    color: var(--accent-blue);
    text-decoration: underline;
  }

  .draft-expand {
    justify-self: start;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--accent-blue);
    font-size: var(--font-size-xs);
    font-weight: 600;
    line-height: 1.4;
    cursor: pointer;
  }

  .draft-expand:hover {
    text-decoration: underline;
  }

  .draft-actions {
    display: flex;
    gap: 4px;
  }
</style>
