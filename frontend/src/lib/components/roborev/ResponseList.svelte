<script lang="ts">
  import { getStores } from "../../context.js";
  import { formatRelativeTime } from "@kenn-io/kit-ui";
  const stores = getStores();
  let commentText = $state("");
  let submitting = $state(false);

  $effect(() => {
    void stores.roborevReview?.getSelectedJobId();
    commentText = "";
  });

  function handleSubmit(): void {
    const review = stores.roborevReview;
    const jobId =
      review?.getSelectedJobId();
    const comment = commentText.trim();
    if (!review || !jobId || !comment) return;
    submitting = true;
    review.addComment(jobId, comment, {
      onSuccess: () => {
        commentText = "";
        submitting = false;
      },
      onFailure: () => {
        submitting = false;
      },
    });
  }

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      handleSubmit();
    }
  }
</script>

<div class="response-list">
  {#if stores.roborevReview}
    {@const responses = stores.roborevReview.getResponses()}
    {#if responses.length > 0}
      <div class="responses">
        {#each responses as resp (resp.id)}
          <div class="response-item">
            <div class="response-header">
              <span class="responder">
                {resp.responder}
              </span>
              <span
                class="timestamp"
                title={resp.created_at}
              >
                {formatRelativeTime(resp.created_at)}
              </span>
            </div>
            <div class="response-body">
              {resp.response}
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <div class="no-responses">No comments yet.</div>
    {/if}

    {#if !stores.roborevReview.isClosed()}
      <div class="comment-input">
        <textarea
          class="comment-textarea"
          placeholder="Add a comment..."
          bind:value={commentText}
          onkeydown={handleKeydown}
          disabled={submitting}
        ></textarea>
        <button
          class="submit-btn"
          disabled={submitting || !commentText.trim()}
          onclick={handleSubmit}
        >
          {submitting ? "Sending..." : "Comment"}
        </button>
      </div>
    {/if}
  {/if}
</div>

<style>
  .response-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .responses {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .response-item {
    padding: 8px 12px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
  }

  .response-header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 4px;
  }

  .responder {
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
  }

  .timestamp {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .response-body {
    font-size: var(--font-size-md);
    color: var(--text-secondary);
    line-height: 1.5;
    white-space: pre-wrap;
  }

  .no-responses {
    padding: 12px 0;
    font-size: var(--font-size-sm);
    color: var(--text-muted);
    text-align: center;
  }

  /* Submit sits inside the field, matching the pull request and issue
   * comment boxes; the textarea reserves its footprint as bottom padding. */
  .comment-input {
    position: relative;
    padding-top: 8px;
    border-top: 1px solid var(--border-muted);
  }

  .comment-textarea {
    display: block;
    width: 100%;
    padding: 6px 10px
      calc(var(--focus-detail-hit-target, 39.5px) + var(--focus-detail-space-sm, 7.5px));
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-family: inherit;
    line-height: 1.4;
    resize: vertical;
    outline: none;
    min-height: 80px;
    max-height: 200px;
  }

  .comment-textarea::placeholder {
    color: var(--text-muted);
  }

  .comment-textarea:focus {
    border-color: var(--accent-blue);
  }

  .comment-textarea:disabled {
    opacity: var(--opacity-disabled);
  }

  .submit-btn {
    position: absolute;
    right: var(--focus-detail-space-sm, 8px);
    bottom: var(--focus-detail-space-sm, 8px);
    padding: var(--focus-detail-space-xs, 6px) var(--focus-detail-space-md, 14px);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--accent-blue);
    color: var(--text-on-accent);
    font-size: var(--font-size-sm);
    font-weight: 500;
    cursor: pointer;
    white-space: nowrap;
    z-index: 1;
    transition: opacity 0.15s;
  }

  .submit-btn:hover:enabled {
    opacity: 0.85;
  }

  .submit-btn:disabled {
    opacity: var(--opacity-disabled);
    cursor: not-allowed;
  }
</style>
