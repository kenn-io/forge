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

  async function handleSubmit(): Promise<void> {
    const jobId =
      stores.roborevReview?.getSelectedJobId();
    if (!jobId || !commentText.trim()) return;
    submitting = true;
    try {
      const ok =
        await stores.roborevReview?.addComment(
          jobId,
          commentText.trim(),
        );
      if (ok) commentText = "";
    } finally {
      submitting = false;
    }
  }

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      void handleSubmit();
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
          rows="2"
        ></textarea>
        <button
          class="submit-btn"
          disabled={submitting || !commentText.trim()}
          onclick={() => void handleSubmit()}
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

  .comment-input {
    display: flex;
    gap: 8px;
    align-items: flex-end;
    padding-top: 8px;
    border-top: 1px solid var(--border-muted);
  }

  .comment-textarea {
    flex: 1;
    padding: 6px 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-family: inherit;
    line-height: 1.4;
    resize: vertical;
    outline: none;
    min-height: 36px;
  }

  .comment-textarea::placeholder {
    color: var(--text-muted);
  }

  .comment-textarea:focus {
    border-color: var(--accent-blue);
  }

  .comment-textarea:disabled {
    opacity: 0.6;
  }

  .submit-btn {
    padding: 6px 14px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--accent-blue);
    color: var(--text-on-accent);
    font-size: var(--font-size-sm);
    font-weight: 500;
    cursor: pointer;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .submit-btn:hover:not(:disabled) {
    opacity: 0.9;
  }

  .submit-btn:disabled {
    opacity: 0.5;
    cursor: default;
  }
</style>
