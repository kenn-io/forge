<script lang="ts">
  import type {
    MessageDetailData,
    MessageSummary,
  } from "../../api/messages/types";
  import type { IssueRef, KataAPI } from "../../messages/types";
  import type { MessageLinkInput } from "../../messages/messageLinks";
  import { selectInclusiveWindow } from "../../messages/threadWindow";
  import MessageDetail from "./MessageDetail.svelte";

  interface Props {
    detail: MessageDetailData | null;
    thread: MessageSummary[] | null;
    selectedMessageId: number | null;
    onSelectMessage: (id: number) => void;
    loadingDetail: boolean;
    detailError: string | null;
    loadingThread: boolean;
    threadError: string | null;
    // Forwarded to MessageDetail in every mode.
    permalinkOf: (id: number) => string;
    remoteImageURL: (id: number, token: string, index: string) => string;
    kata?: Pick<KataAPI, "search"> | undefined;
    onLinkMessage?: ((
      issueUid: string,
      input: MessageLinkInput,
    ) => Promise<{ qualified_id: string }>) | undefined;
    reverseLinks?: IssueRef[] | undefined;
    onOpenIssue?: ((uid: string) => void) | undefined;
    // v2a: per-message image opt-in + view-mode forwarded to MessageDetail (T9).
    imagesLoaded?: boolean | undefined;
    onLoadImages?: ((id: number, token: string) => void) | undefined;
    viewMode?: "html" | "text" | undefined;
    onViewModeChange?: ((id: number, mode: "html" | "text") => void) | undefined;
    remoteImageCount?: number | undefined;
    remoteImageToken?: string | undefined;
    htmlSanitizationFailed?: boolean | undefined;
  }

  let {
    detail,
    thread,
    selectedMessageId,
    onSelectMessage,
    loadingDetail,
    detailError,
    loadingThread,
    threadError,
    permalinkOf,
    remoteImageURL,
    kata,
    onLinkMessage,
    reverseLinks,
    onOpenIssue,
    imagesLoaded,
    onLoadImages,
    viewMode,
    onViewModeChange,
    remoteImageCount,
    remoteImageToken,
    htmlSanitizationFailed,
  }: Props = $props();

  // The selected thread row (when present in the thread). Reused below.
  const selectedRow = $derived.by<MessageSummary | null>(() => {
    if (thread === null || selectedMessageId === null) return null;
    return thread.find((m) => m.id === selectedMessageId) ?? null;
  });

  // Detail is trustworthy only when it belongs to the currently-selected
  // message; otherwise it is stale from the prior selection (the detail
  // fetch hasn't caught up to the new route yet).
  const detailMatchesSelected = $derived(
    detail !== null && detail.id === selectedMessageId,
  );

  // Pick the rendered window for stack mode. Stack mode requires the
  // selected message to actually appear in the window - if it doesn't
  // (e.g., stale thread from a sibling conversation during a conv-swap
  // race), fall through so non-stack modes can render a standalone
  // MessageDetail instead of a stack with no open card.
  const windowed = $derived.by(() => {
    if (thread === null || selectedMessageId === null) return null;
    if (thread.length <= 1) return null;
    const w = selectInclusiveWindow(thread, selectedMessageId);
    if (!w.messages.some((m) => m.id === selectedMessageId)) return null;
    return w;
  });

  // Subject for the thread header row: prefer the live detail (only when
  // it belongs to the currently-selected message), otherwise fall back to
  // the selected thread row's subject. This avoids displaying the prior
  // message's subject while a same-thread navigation is in flight.
  const headerSubject = $derived.by(() => {
    if (detailMatchesSelected) return detail!.subject;
    return selectedRow?.subject ?? "";
  });

  // Whether either the live detail OR the selected thread row has
  // resolved. Tracking this separately from `headerSubject !== ""` means
  // a valid message whose subject happens to be empty still renders the
  // header row (with an empty subject) rather than being mistaken for an
  // unresolved race.
  const headerResolved = $derived(detailMatchesSelected || selectedRow !== null);

  // Mode: stack only when the window is non-null. Everything else degrades
  // to a non-compact MessageDetail (with an optional inline notice on top).
  const mode = $derived.by(() => {
    if (threadError !== null) return "error" as const;
    if (windowed !== null) return "stack" as const;
    if (
      thread !== null &&
      thread.length === 1 &&
      thread[0]!.id === selectedMessageId
    ) return "singleton" as const;
    return "loading" as const;
  });

  // Visible text for the collapsed peer button. The aria-label
  // separately carries the raw ISO sent_at so the test query is
  // locale-stable; this string is intentionally locale-aware to
  // match the user's locale (mirrors MessageDetail's formatDate).
  function formatPeerName(m: MessageSummary): string {
    const date = new Date(m.sent_at);
    const dateStr = date.toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
    return `${m.from}  -  ${dateStr}  -  ${m.subject}`;
  }
</script>

<section class="messages-thread" aria-label="Conversation">
  {#if mode === "stack" && windowed !== null}
    {#if headerResolved}
      <header class="thread-header">
        <h2 class="thread-subject">{headerSubject}</h2>
        <span class="thread-count">{windowed.messages.length} msgs</span>
      </header>
    {/if}
    {#if windowed.truncated}
      <p class="thread-truncated" role="status">
        Showing {windowed.messages.length} of {thread!.length} messages. The selected message is included.
      </p>
    {/if}
    <div class="thread-scroll">
      {#each windowed.messages as m (m.id)}
        {#if m.id === selectedMessageId}
          <div class="thread-card open">
            <MessageDetail
              {detail}
              loading={loadingDetail}
              error={detailError}
              {permalinkOf}
              {remoteImageURL}
              {kata}
              {onLinkMessage}
              {reverseLinks}
              {onOpenIssue}
              compact={true}
              {imagesLoaded}
              {onLoadImages}
              {viewMode}
              {onViewModeChange}
              {remoteImageCount}
              {remoteImageToken}
              {htmlSanitizationFailed}
            />
          </div>
        {:else}
          <button
            type="button"
            class="thread-peer"
            aria-label={`Open message from ${m.from} sent ${m.sent_at}`}
            onclick={() => onSelectMessage(m.id)}
          >{formatPeerName(m)}</button>
        {/if}
      {/each}
    </div>
  {:else}
    {#if mode === "error"}
      <p class="thread-error" role="alert">
        Couldn't load conversation context. Showing this message only.
      </p>
    {:else if mode === "loading" && loadingThread}
      <p class="thread-loading" role="status">Loading conversation...</p>
    {/if}
    <MessageDetail
      {detail}
      loading={loadingDetail}
      error={detailError}
      {permalinkOf}
      {remoteImageURL}
      {kata}
      {onLinkMessage}
      {reverseLinks}
      {onOpenIssue}
      compact={false}
      {imagesLoaded}
      {onLoadImages}
      {viewMode}
      {onViewModeChange}
      {remoteImageCount}
      {remoteImageToken}
      {htmlSanitizationFailed}
    />
  {/if}
</section>

<style>
  .messages-thread {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    overflow: hidden;
    background: var(--bg-surface);
  }

  .thread-header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 20px 8px;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .thread-subject {
    margin: 0;
    font-size: var(--font-size-md);
    font-weight: 600;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .thread-count {
    flex-shrink: 0;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .thread-truncated {
    margin: 0;
    padding: 6px 20px;
    background: var(--bg-inset);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .thread-error,
  .thread-loading {
    margin: 0;
    padding: 6px 20px;
    font-size: var(--font-size-xs);
    flex-shrink: 0;
    border-bottom: 1px solid var(--border-muted);
  }

  .thread-error {
    background: var(--accent-red-soft);
    color: var(--accent-red);
  }

  .thread-loading {
    background: var(--bg-inset);
    color: var(--text-muted);
  }

  .thread-scroll {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 8px 0 16px;
  }

  .thread-card.open {
    border-top: 1px solid var(--border-muted);
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-surface);
  }

  .thread-peer {
    display: block;
    width: 100%;
    text-align: left;
    padding: 8px 20px;
    border: 0;
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    font-size: var(--font-size-xs);
    cursor: pointer;
    border-top: 1px solid var(--border-muted);
    transition: background 0.07s;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .thread-peer:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .thread-peer:focus-visible {
    outline: none;
    background: var(--accent-blue-soft);
    color: var(--text-primary);
  }
</style>
