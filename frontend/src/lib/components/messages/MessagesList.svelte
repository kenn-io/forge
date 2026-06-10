<script lang="ts">
  import { PaperclipIcon } from "../../icons";
  import { relativeTime } from "../../api/dates";
  import type { MessageSummary } from "../../api/messages/types";

  interface Props {
    messages: readonly MessageSummary[];
    selectedID: number | null;
    loading: boolean;
    onSelect: (id: number) => void;
  }

  let { messages, selectedID, loading, onSelect }: Props = $props();

  let listBody: HTMLDivElement | null = $state(null);

  function focusRow(target: HTMLElement | null): void {
    if (!target) return;
    target.focus();
    // Auto-select on focus move - detail pane follows the list cursor,
    // matching IssueList / Linear / Messages.app browse-mode semantics.
    target.click();
  }

  function handleListKeydown(event: KeyboardEvent): void {
    const target = event.target;
    if (!(target instanceof HTMLElement) || !target.classList.contains("row")) return;
    if (!listBody) return;
    if (event.metaKey || event.ctrlKey || event.altKey) return;

    const rowEls = Array.from(listBody.querySelectorAll<HTMLElement>("button.row"));
    const idx = rowEls.indexOf(target);
    if (idx === -1) return;

    let nextIdx: number | null = null;
    switch (event.key) {
      case "ArrowDown":
      case "j":
        nextIdx = Math.min(rowEls.length - 1, idx + 1);
        break;
      case "ArrowUp":
      case "k":
        nextIdx = Math.max(0, idx - 1);
        break;
      case "Home":
        nextIdx = 0;
        break;
      case "End":
        nextIdx = rowEls.length - 1;
        break;
      case "Enter": {
        // Idempotent: the row is already selected via auto-select on focus
        // move. Still call onSelect so first-mount keyboard users (Tab then
        // Enter) get the detail pane to open.
        const idStr = target.dataset["id"];
        if (idStr !== undefined) {
          event.preventDefault();
          onSelect(Number(idStr));
        }
        return;
      }
      default:
        return;
    }

    event.preventDefault();
    // Boundary case: j on last row / k on first row resolves to the same
    // index - skip to avoid double-firing the click / onSelect.
    if (nextIdx === idx) return;
    focusRow(rowEls[nextIdx] ?? null);
  }
</script>

<div class="messages-list" role="region" aria-label="Messages">
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="list-body" bind:this={listBody} onkeydown={handleListKeydown}>
    {#if loading}
      {#each { length: 4 } as _, i (i)}
        <div class="skeleton-row" aria-hidden="true">
          <span class="skel skel-sender"></span>
          <span class="skel skel-subject"></span>
          <span class="skel skel-snippet"></span>
        </div>
      {/each}
    {:else if messages.length === 0}
      <div class="empty">No messages match your search.</div>
    {:else}
      {#each messages as msg (msg.id)}
        <button
          class="row"
          class:selected={msg.id === selectedID}
          aria-current={msg.id === selectedID ? "true" : undefined}
          data-id={msg.id}
          type="button"
          onclick={() => onSelect(msg.id)}
        >
          <span class="cell cell-sender">{msg.from}</span>
          <span class="cell cell-subject">{msg.subject}</span>
          <span class="cell cell-snippet">{msg.snippet}</span>
          <span class="cell cell-date">{relativeTime(msg.sent_at)}</span>
          {#if msg.has_attachments}
            <span class="cell cell-clip" aria-label="Has attachments">
              <PaperclipIcon size={12} strokeWidth={1.75} />
            </span>
          {/if}
        </button>
      {/each}
    {/if}
  </div>
</div>

<style>
  .messages-list {
    display: flex;
    flex-direction: column;
    min-height: 0;
    overflow: hidden;
    background: var(--bg-primary);
    height: 100%;
  }

  .list-body {
    flex: 1;
    overflow-y: auto;
    padding: 4px 0;
  }

  /* ---- message rows ---- */

  .row {
    display: grid;
    grid-template-columns: minmax(120px, 18%) minmax(120px, 22%) 1fr auto auto;
    gap: 8px;
    align-items: baseline;
    width: 100%;
    padding: 5px 12px;
    border: 0;
    background: transparent;
    color: inherit;
    text-align: left;
    cursor: pointer;
    border-radius: 0;
    min-height: 30px;
    transition: background 0.07s;
  }

  .row:hover {
    background: var(--bg-surface-hover);
  }

  .row.selected {
    background: var(--accent-blue-soft);
  }

  .row:focus-visible {
    outline: none;
    background: var(--accent-blue-soft);
  }

  .cell {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-xs);
  }

  .cell-sender {
    color: var(--text-primary);
    font-weight: 500;
    font-size: var(--font-size-xs);
  }

  .cell-subject {
    color: var(--text-primary);
    font-size: var(--font-size-xs);
    font-weight: 600;
  }

  .cell-snippet {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .cell-date {
    color: var(--text-faint);
    font-size: var(--font-size-2xs);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .cell-clip {
    display: inline-flex;
    align-items: center;
    color: var(--text-muted);
    flex-shrink: 0;
  }

  /* ---- empty state ---- */

  .empty {
    padding: 32px 16px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  /* ---- skeleton loading state ---- */

  .skeleton-row {
    display: grid;
    grid-template-columns: 18% 22% 1fr;
    gap: 8px;
    align-items: center;
    padding: 6px 12px;
    min-height: 30px;
  }

  .skel {
    display: block;
    height: 11px;
    border-radius: 3px;
    background: var(--bg-inset, #e5e7eb);
    animation: shimmer 1.4s ease-in-out infinite;
  }

  .skel-sender { width: 80%; }
  .skel-subject { width: 90%; }
  .skel-snippet { width: 70%; }

  @keyframes shimmer {
    0%   { opacity: 0.5; }
    50%  { opacity: 1; }
    100% { opacity: 0.5; }
  }
</style>
