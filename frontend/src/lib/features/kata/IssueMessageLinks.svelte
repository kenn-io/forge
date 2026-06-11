<script lang="ts">
  import type { MessageLinkRef } from "../../messages/types";

  interface Props {
    links: MessageLinkRef[];
    busyIds?: ReadonlySet<number> | undefined;
    onOpenMessage?: ((link: MessageLinkRef) => void) | undefined;
    onUnlink: (link: MessageLinkRef) => void;
  }

  let {
    links,
    busyIds = undefined,
    onOpenMessage = undefined,
    onUnlink,
  }: Props = $props();

  function truncate(value: string, max: number): string {
    return value.length > max ? `${value.slice(0, max - 3)}...` : value;
  }

  function displaySubject(subject: string, max: number): string {
    return subject.trim().length === 0 ? "(no subject)" : truncate(subject, max);
  }

  function formatFrom(value: string): string {
    const lt = value.indexOf("<");
    if (lt > 0) return value.slice(0, lt).trim();
    return value.trim();
  }

  function parseMessageDate(value: string): Date | null {
    const dateOnly = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
    if (dateOnly !== null) {
      const [, year, month, day] = dateOnly;
      return new Date(Number(year), Number(month) - 1, Number(day));
    }
    const ts = Date.parse(value);
    return Number.isNaN(ts) ? null : new Date(ts);
  }

  function formatDate(iso: string): string {
    const date = parseMessageDate(iso);
    if (date === null) return iso;
    const days = Math.floor((Date.now() - date.getTime()) / 86_400_000);
    if (days === 0) return "today";
    if (days === 1) return "yesterday";
    if (days > 1 && days < 7) return `${days}d ago`;
    return new Intl.DateTimeFormat(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    }).format(date);
  }
</script>

{#if links.length > 0}
  <section class="issue-messages-links" aria-label="Linked messages">
    <div class="section-head">
      <h3>Messages</h3>
    </div>
    <ul class="pill-list">
      {#each links as link (link.message_id)}
        {@const busy = busyIds?.has(link.message_id) ?? false}
        {@const canOpen = onOpenMessage !== undefined}
        {@const from = formatFrom(link.from)}
        {@const subjectLabel = displaySubject(link.subject, 60)}
        <li class="pill" class:pill--disabled={!canOpen}>
          <button
            type="button"
            class="pill-open"
            disabled={!canOpen || busy}
            title={canOpen ? `Open ${from} - ${subjectLabel}` : "Messages mode is not enabled."}
            onclick={() => onOpenMessage?.(link)}
          >
            <span class="pill-subject">{subjectLabel}</span>
            <span class="pill-from">{from}</span>
            <span class="pill-date">{formatDate(link.sent_at)}</span>
          </button>
          <button
            type="button"
            class="pill-unlink"
            aria-label={`Unlink ${subjectLabel}`}
            disabled={busy}
            onclick={() => onUnlink(link)}
          >x</button>
        </li>
      {/each}
    </ul>
  </section>
{/if}

<style>
  .issue-messages-links {
    padding-top: 18px;
    margin-top: 18px;
    border-top: 1px solid var(--border-muted);
  }

  .section-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    margin-bottom: 10px;
  }

  .section-head h3 {
    margin: 0;
    font-size: var(--font-size-sm);
    font-weight: 650;
    color: var(--text-primary);
  }

  .pill-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    list-style: none;
    padding: 0;
    margin: 0;
  }

  .pill {
    display: inline-flex;
    align-items: stretch;
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    border: 1px solid transparent;
    overflow: hidden;
    transition: background 0.1s, border-color 0.1s;
  }

  .pill:hover {
    background: var(--bg-surface-hover);
    border-color: var(--border-default);
  }

  .pill--disabled {
    opacity: 0.72;
  }

  .pill-open {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    flex: 1;
    min-width: 0;
    padding: 6px 10px;
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    line-height: 1.25;
    text-align: left;
    border: 0;
  }

  .pill-open:disabled {
    cursor: not-allowed;
  }

  .pill-open:not(:disabled) {
    cursor: pointer;
  }

  .pill-open:not(:disabled):hover {
    color: var(--accent-blue);
  }

  .pill-subject {
    flex: 1;
    min-width: 0;
    font-weight: 550;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pill-from {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 30%;
  }

  .pill-date {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
    flex: 0 0 auto;
  }

  .pill-unlink {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    padding: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1;
    background: transparent;
    border: 0;
    border-left: 1px solid var(--border-muted);
    cursor: pointer;
  }

  .pill-unlink:not(:disabled):hover {
    color: var(--accent-red, #c14a3c);
    background: var(--bg-surface-hover);
  }

  .pill-unlink:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
