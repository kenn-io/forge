<script lang="ts">
  import type { LinkedMessageRow } from "../../messages/reverseLinks";

  interface Props {
    rows: LinkedMessageRow[] | null;
    loading: boolean;
    error: string | null;
    selectedMessageId: number | null;
    onRefresh: () => void;
    onSelectMessage: (messageId: number) => void;
    onOpenIssue: (uid: string) => void;
    MAX_ROWS?: number;
  }

  let {
    rows,
    loading,
    error,
    selectedMessageId,
    onRefresh,
    onSelectMessage,
    onOpenIssue,
    MAX_ROWS = 200,
  }: Props = $props();

  function truncate(s: string, n: number): string {
    return s.length > n ? s.slice(0, n - 1) + "..." : s;
  }

  function displaySubject(subject: string, max: number): string {
    return subject.trim().length === 0 ? "(no subject)" : truncate(subject, max);
  }

  function formatFrom(s: string): string {
    // "Alice <alice@example.com>" -> "Alice"; bare addresses pass through.
    const lt = s.indexOf("<");
    if (lt > 0) return s.slice(0, lt).trim();
    return s.trim();
  }

  function formatDate(iso: string): string {
    const ts = Date.parse(iso);
    if (Number.isNaN(ts)) return iso;
    return new Intl.DateTimeFormat(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    }).format(new Date(ts));
  }
</script>

<section class="linked-messages" aria-label="Linked messages">
  <header class="linked-head">
    <h2>Linked messages</h2>
    <button type="button" class="refresh" onclick={onRefresh} disabled={loading}>
      {loading ? "Loading..." : "Refresh"}
    </button>
  </header>

  {#if error}
    <p role="alert" class="linked-error">{error}</p>
  {/if}

  {#if loading}
    <div class="linked-state">Loading...</div>
  {:else if rows === null}
    <!-- Not loading and never produced rows: if error, the banner above +
         Refresh ARE the recovery UI (no "Loading..."); if no error, this is the
         brief pre-load tick, so a neutral Loading... is fine. -->
    {#if !error}
      <div class="linked-state">Loading...</div>
    {/if}
  {:else if rows.length === 0}
    <div class="linked-empty">No linked messages yet.</div>
  {:else}
    {@const visible = rows.slice(0, MAX_ROWS)}
    <div class="linked-body">
      <table class="linked-table">
        <thead>
          <tr>
            <th scope="col">Subject</th>
            <th scope="col">From</th>
            <th scope="col">Sent</th>
            <th scope="col">Linked tasks</th>
          </tr>
        </thead>
        <tbody>
          {#each visible as row (row.message.message_id)}
            <tr class:active={selectedMessageId === row.message.message_id}>
              <td>
                <button
                  type="button"
                  class="row-subject"
                  onclick={() => onSelectMessage(row.message.message_id)}
                >{displaySubject(row.message.subject, 80)}</button>
              </td>
              <td class="row-from">{formatFrom(row.message.from)}</td>
              <td class="row-sent">{formatDate(row.message.sent_at)}</td>
              <td class="row-issues">
                <ul class="chip-list">
                  {#each row.issues as issue (issue.uid)}
                    <li>
                      <button
                        type="button"
                        class="issue-chip"
                        onclick={() => onOpenIssue(issue.uid)}
                        title={issue.title}
                      >{issue.qualified_id}</button>
                    </li>
                  {/each}
                </ul>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if rows.length > visible.length}
        <p class="linked-truncated">Showing {visible.length} of {rows.length}. Refine via Tasks to see more.</p>
      {/if}
    </div>
  {/if}
</section>

<style>
  .linked-messages {
    display: flex;
    flex-direction: column;
    gap: 0;
    height: 100%;
    overflow: hidden;
    background: var(--bg-surface);
    font-size: var(--font-size-sm);
  }

  .linked-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 16px 8px;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .linked-head h2 {
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-primary);
  }

  .refresh {
    padding: 3px 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    cursor: pointer;
    transition: background 0.07s, color 0.07s;
  }

  .refresh:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .refresh:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .linked-error {
    margin: 8px 16px;
    padding: 6px 10px;
    border-radius: var(--radius-sm);
    background: var(--accent-red-soft);
    color: var(--accent-red);
    font-size: var(--font-size-xs);
  }

  .linked-state,
  .linked-empty {
    padding: 32px 16px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: center;
  }

  .linked-body {
    flex: 1;
    min-height: 0;
    /* `overflow: auto` (not just overflow-y) so wide tables - long subjects,
       many chips per row - get a horizontal scrollbar instead of being
       clipped by the parent's `overflow: hidden`. */
    overflow: auto;
  }

  .linked-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--font-size-xs);
  }

  .linked-table thead th {
    padding: 6px 12px;
    text-align: left;
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
    position: sticky;
    top: 0;
    z-index: 1;
  }

  .linked-table tbody tr {
    border-bottom: 1px solid var(--border-muted);
    transition: background 0.07s;
  }

  .linked-table tbody tr:hover {
    background: var(--bg-surface-hover);
  }

  .linked-table tbody tr.active {
    background: var(--accent-blue-soft);
    box-shadow: inset 2px 0 0 var(--accent-blue);
  }

  .linked-table tbody tr.active .row-subject {
    font-weight: 600;
  }

  .linked-table td {
    padding: 7px 12px;
    vertical-align: middle;
    color: var(--text-secondary);
  }

  .row-subject {
    background: none;
    border: none;
    padding: 0;
    margin: 0;
    font: inherit;
    font-size: var(--font-size-xs);
    color: var(--accent-blue);
    cursor: pointer;
    text-align: left;
    text-decoration: underline;
    text-underline-offset: 2px;
    text-decoration-color: transparent;
    transition: text-decoration-color 0.1s;
  }

  .row-subject:hover {
    text-decoration-color: currentColor;
  }

  .row-from {
    color: var(--text-secondary);
    white-space: nowrap;
    max-width: 160px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .row-sent {
    white-space: nowrap;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .row-issues {
    min-width: 120px;
  }

  .chip-list {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .issue-chip {
    display: inline-block;
    padding: 2px 7px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--accent-blue);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    line-height: 1.5;
    cursor: pointer;
    transition: background 0.07s, border-color 0.07s;
  }

  .issue-chip:hover {
    background: var(--accent-blue-soft);
    border-color: var(--accent-blue);
  }

  .linked-truncated {
    padding: 8px 16px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-style: italic;
    text-align: center;
    border-top: 1px solid var(--border-muted);
  }
</style>
