<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import CopyIcon from "@lucide/svelte/icons/copy";
  import MessageSquareReplyIcon from "@lucide/svelte/icons/message-square-reply";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import XIcon from "@lucide/svelte/icons/x";
  import { untrack } from "svelte";
  import { slide } from "svelte/transition";
  import type { IssueEvent, PREvent } from "../../api/types.js";
  import type { StoreInstances } from "../../types.js";
  import { renderMarkdown } from "../../utils/markdown.js";
  import { timeAgo } from "../../utils/time.js";
  import { copyToClipboard } from "../../utils/clipboard.js";
  import { getStores } from "../../context.js";
  import {
    buildItemReferenceLink,
    type ItemReferenceDataAttributes,
  } from "../../utils/item-reference.js";
  import CommentEditor from "./CommentEditor.svelte";
  import DiffReviewThreadSnippet from "../diff/DiffReviewThreadSnippet.svelte";
  import {
    reviewThreadContext,
    type ReviewThread,
  } from "../diff/review-thread-context.js";

  interface Props {
    events: Array<PREvent | IssueEvent>;
    provider?: string | undefined;
    platformHost?: string | undefined;
    repoOwner?: string;
    repoName?: string;
    repoPath?: string | undefined;
    number?: number | undefined;
    canResolveReviewThreads?: boolean;
    canReplyToThreads?: boolean;
    filtered?: boolean;
    showCommitDetails?: boolean;
    onEditComment?: ((event: PREvent | IssueEvent, body: string) => Promise<boolean>) | undefined;
    jumpToReviewThread?: ((thread: ReviewThread) => void) | undefined;
  }

  const {
    events,
    provider,
    platformHost,
    repoOwner,
    repoName,
    repoPath,
    number = undefined,
    canResolveReviewThreads = false,
    canReplyToThreads = false,
    filtered = false,
    showCommitDetails = true,
    onEditComment,
    jumpToReviewThread,
  }: Props = $props();
  const stores = getStores() as StoreInstances | undefined;
  const detailStore = stores?.detail;
  const diffStore = stores?.diff;
  const diffReviewDraft = stores?.diffReviewDraft;
  const diff = $derived(diffStore?.getDiff() ?? null);

  $effect(() => {
    if (!provider || !repoOwner || !repoName || !repoPath || number == null) return;
    const nextRef = { provider, platformHost, owner: repoOwner, name: repoName, repoPath };
    const nextNumber = number;
    untrack(() => {
      diffReviewDraft?.setRouteContext(nextRef, nextNumber);
    });
  });

  $effect(() => {
    if (!diffStore || !provider || !repoOwner || !repoName || !repoPath || number == null) return;
    if (!events.some((event) => reviewThreadFor(event) !== null)) return;
    if (diffStore.isDiffLoading()) return;
    const current = diffStore.getCurrentPR();
    if (
      diffStore.getDiff() !== null &&
      current?.provider === provider &&
      current.platformHost === platformHost &&
      current?.owner === repoOwner &&
      current.name === repoName &&
      current.repoPath === repoPath &&
      current.number === number
    ) {
      return;
    }
    untrack(() => {
      void diffStore.loadDiff(repoOwner, repoName, number, {
        provider,
        platformHost,
        owner: repoOwner,
        name: repoName,
        repoPath,
      });
    });
  });

  const typeLabels: Record<string, string> = {
    issue_comment: "Comment",
    comment_deleted: "Comment deleted",
    review: "Review",
    commit: "Commit",
    force_push: "Force-pushed",
    review_comment: "Review Comment",
    assigned: "Assigned",
    unassigned: "Unassigned",
  };

  const typeColors: Record<string, string> = {
    issue_comment: "var(--accent-blue)",
    comment_deleted: "var(--text-muted)",
    review: "var(--accent-purple)",
    review_comment: "var(--accent-purple)",
    commit: "var(--accent-green)",
    force_push: "var(--accent-red)",
    assigned: "var(--accent-blue)",
    unassigned: "var(--text-muted)",
  };

  function shouldRenderMarkdown(eventType: string): boolean {
    return eventType === "issue_comment" || eventType === "review" || eventType === "review_comment";
  }

  type TimelineEntry = {
    key: string;
    event: PREvent | IssueEvent;
    threadID?: string | undefined;
    reviewThread?: TimelineReviewThread | undefined;
    replies: Array<PREvent | IssueEvent>;
  };

  type TimelineReviewThread = {
    thread: ReviewThread;
  };

  function threadID(event: PREvent | IssueEvent): string | null {
    return typeof event.ThreadID === "string" && event.ThreadID.length > 0
      ? event.ThreadID
      : null;
  }

  function timelineThreadID(event: PREvent | IssueEvent): string | null {
    return threadID(event) ?? reviewThreadFor(event)?.thread.id ?? null;
  }

  function isThreadedComment(event: PREvent | IssueEvent): boolean {
    return shouldRenderMarkdown(event.EventType) && timelineThreadID(event) !== null;
  }

  function eventSortValue(event: PREvent | IssueEvent): number {
    const timestamp = Date.parse(event.CreatedAt);
    return Number.isFinite(timestamp) ? timestamp : 0;
  }

  function compareEventsAscending(a: PREvent | IssueEvent, b: PREvent | IssueEvent): number {
    return eventSortValue(a) - eventSortValue(b) || a.ID - b.ID;
  }

  function compareEventsDescending(a: PREvent | IssueEvent, b: PREvent | IssueEvent): number {
    return eventSortValue(b) - eventSortValue(a) || b.ID - a.ID;
  }

  function buildTimelineEntries(sourceEvents: Array<PREvent | IssueEvent>): TimelineEntry[] {
    const threads: Array<{ id: string; events: Array<PREvent | IssueEvent> }> = [];

    for (const event of sourceEvents) {
      const id = timelineThreadID(event);
      if (!id || !isThreadedComment(event)) continue;
      const thread = threads.find((item) => item.id === id);
      if (thread) {
        thread.events = [...thread.events, event];
      } else {
        threads.push({ id, events: [event] });
      }
    }

    const emittedThreads: string[] = [];
    const entries: TimelineEntry[] = [];

    for (const event of sourceEvents) {
      const id = timelineThreadID(event);
      if (!id || !isThreadedComment(event)) {
        entries.push({
          key: `event-${event.ID}`,
          event,
          reviewThread: reviewThreadFor(event) ?? undefined,
          replies: [],
        });
        continue;
      }

      if (emittedThreads.includes(id)) continue;
      emittedThreads.push(id);

      const threadEvents = [...(threads.find((item) => item.id === id)?.events ?? [event])];
      const sortedThreadEvents = threadEvents.sort(compareEventsAscending);
      const reviewThread = sortedThreadEvents
        .map((threadEvent) => reviewThreadFor(threadEvent))
        .find((thread): thread is TimelineReviewThread => thread !== null);
      if (threadEvents.length === 1) {
        entries.push({
          key: `event-${event.ID}`,
          event,
          reviewThread,
          replies: [],
        });
        continue;
      }

      const [root, ...replies] = sortedThreadEvents;
      entries.push({
        key: `thread-${id}`,
        event: root ?? event,
        threadID: id,
        reviewThread,
        replies: replies.sort(compareEventsDescending),
      });
    }

    return entries;
  }

  const timelineEntries = $derived(buildTimelineEntries(events));

  function isCompactEvent(eventType: string): boolean {
    return (
      eventType === "commit" ||
      eventType === "comment_deleted" ||
      eventType === "force_push" ||
      eventType === "cross_referenced" ||
      eventType === "renamed_title" ||
      eventType === "base_ref_changed" ||
      eventType === "assigned" ||
      eventType === "unassigned"
    );
  }

  function shortCommit(summary: string): string {
    return summary.length > 7 ? summary.slice(0, 7) : summary;
  }

  function commitTitle(body: string): string {
    return body.split(/\r?\n/, 1)[0] ?? "";
  }

  function commitDetailsBody(body: string): string {
    return body.trim();
  }

  function systemEventLabel(eventType: string): string {
    switch (eventType) {
      case "cross_referenced":
        return "Referenced";
      case "comment_deleted":
        return "Comment deleted";
      case "renamed_title":
        return "Title changed";
      case "base_ref_changed":
        return "Base changed";
      case "assigned":
        return "Assigned";
      case "unassigned":
        return "Unassigned";
      case "force_push":
        return "Force-pushed";
      default:
        return typeLabels[eventType] ?? eventType;
    }
  }

  function parseMetadata(event: PREvent | IssueEvent): Record<string, unknown> {
    if (!event.MetadataJSON) return {};
    try {
      const parsed = JSON.parse(event.MetadataJSON) as unknown;
      if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return {};
      return parsed as Record<string, unknown>;
    } catch {
      return {};
    }
  }

  function metadataString(metadata: Record<string, unknown>, key: string): string | null {
    const value = metadata[key];
    return typeof value === "string" && value.length > 0 ? value : null;
  }

  function metadataNumber(metadata: Record<string, unknown>, key: string): number | null {
    const value = metadata[key];
    if (typeof value === "number" && Number.isInteger(value) && value > 0) return value;
    if (typeof value !== "string") return null;
    const parsed = parseInt(value, 10);
    return Number.isInteger(parsed) && parsed > 0 ? parsed : null;
  }

  type CrossReferenceLink = {
    href: string;
    internal: boolean;
    dataAttributes?: ItemReferenceDataAttributes | undefined;
  };

  function crossReferenceLink(
    metadata: Record<string, unknown>,
    sourceUrl: string | null,
  ): CrossReferenceLink | null {
    const sourceType = metadataString(metadata, "source_type");
    const owner = metadataString(metadata, "source_owner");
    const name = metadataString(metadata, "source_repo");
    const number = metadataNumber(metadata, "source_number");
    if (
      provider &&
      owner &&
      name &&
      number !== null &&
      (sourceType === "PullRequest" || sourceType === "Issue")
    ) {
      const repoPath = `${owner}/${name}`;
      const link = buildItemReferenceLink({
        provider,
        platformHost,
        owner,
        name,
        repoPath,
        number,
        itemType: sourceType === "PullRequest" ? "pr" : "issue",
        externalUrl: sourceUrl ?? undefined,
      });
      return {
        ...link,
        internal: true,
      };
    }
    return sourceUrl ? { href: sourceUrl, internal: false } : null;
  }

  function eventDiffThread(event: PREvent | IssueEvent): ReviewThread | null {
    if (!("diff_thread" in event)) return null;
    return (event.diff_thread as ReviewThread | undefined) ?? null;
  }

  function reviewThreadFor(event: PREvent | IssueEvent): TimelineReviewThread | null {
    const thread = eventDiffThread(event);
    return thread ? { thread } : null;
  }

  async function refreshAfterThreadChange(): Promise<void> {
    if (!provider || !repoOwner || !repoName || !repoPath || number == null) return;
    await detailStore?.refreshDetailOnly(repoOwner, repoName, number, {
      provider,
      platformHost,
      repoPath,
    });
  }

  let copiedId = $state<string | null>(null);
  let copyTimeout: ReturnType<typeof setTimeout> | null = null;
  let editingId = $state<number | null>(null);
  let editDraft = $state("");
  let savingEditId = $state<number | null>(null);
  let editError = $state<string | null>(null);
  let collapsedThreads = $state<string[]>([]);
  let replyingThreadID = $state<string | null>(null);
  let replyDraft = $state("");
  let savingReplyThreadID = $state<string | null>(null);
  let replyError = $state<string | null>(null);

  function canEditComment(event: PREvent | IssueEvent): boolean {
    return (
      event.EventType === "issue_comment" &&
      event.PlatformID != null &&
      repoOwner !== undefined &&
      repoName !== undefined &&
      onEditComment !== undefined
    );
  }

  function startEdit(event: PREvent | IssueEvent): void {
    editingId = event.ID;
    editDraft = event.Body;
    editError = null;
  }

  function cancelEdit(): void {
    editingId = null;
    editDraft = "";
    editError = null;
  }

  function entryThreadID(entry: TimelineEntry): string {
    return entry.threadID ?? String(entry.event.ID);
  }

  function replyTargetID(entry: TimelineEntry): string | null {
    return entry.reviewThread?.thread.id ?? null;
  }

  function canReplyToThread(entry: TimelineEntry): boolean {
    return (
      canReplyToThreads &&
      detailStore !== undefined &&
      provider !== undefined &&
      repoOwner !== undefined &&
      repoName !== undefined &&
      repoPath !== undefined &&
      number !== undefined &&
      replyTargetID(entry) !== null
    );
  }

  function isThreadCollapsed(entry: TimelineEntry): boolean {
    return collapsedThreads.includes(entryThreadID(entry));
  }

  function toggleThread(entry: TimelineEntry): void {
    const id = entryThreadID(entry);
    collapsedThreads = collapsedThreads.includes(id)
      ? collapsedThreads.filter((item) => item !== id)
      : [...collapsedThreads, id];
  }

  function startReply(entry: TimelineEntry): void {
    const targetID = replyTargetID(entry);
    if (!targetID) return;
    replyingThreadID = targetID;
    replyDraft = "";
    replyError = null;
  }

  function cancelReply(): void {
    replyingThreadID = null;
    replyDraft = "";
    replyError = null;
  }

  async function submitReply(entry: TimelineEntry): Promise<void> {
    const targetID = replyTargetID(entry);
    const body = replyDraft.trim();
    if (!targetID || !provider || !repoOwner || !repoName || !repoPath || number === undefined) return;
    if (body === "") {
      replyError = "Reply body must not be empty";
      return;
    }
    savingReplyThreadID = targetID;
    replyError = null;
    try {
      const ok = await detailStore?.replyToDiscussion(repoOwner, repoName, number, targetID, body);
      if (ok) {
        cancelReply();
      } else {
        replyError = detailStore?.getDetailError() ?? "Could not reply to thread";
      }
    } finally {
      savingReplyThreadID = null;
    }
  }

  async function saveEdit(event: PREvent | IssueEvent): Promise<void> {
    const nextBody = editDraft.trim();
    if (nextBody === "") {
      editError = "Comment body must not be empty";
      return;
    }
    if (nextBody === event.Body.trim()) {
      cancelEdit();
      return;
    }
    if (onEditComment === undefined) return;

    savingEditId = event.ID;
    editError = null;
    try {
      const ok = await onEditComment(event, nextBody);
      if (ok) {
        cancelEdit();
      } else {
        editError = "Could not edit comment";
      }
    } finally {
      savingEditId = null;
    }
  }

  function copyText(id: string, text: string): void {
    void copyToClipboard(text).then((ok) => {
      if (!ok) return;
      copiedId = id;
      if (copyTimeout !== null) clearTimeout(copyTimeout);
      copyTimeout = setTimeout(() => {
        copiedId = null;
        copyTimeout = null;
      }, 1500);
    });
  }
</script>

{#snippet eventBody(
  event: PREvent | IssueEvent,
  nested = false,
  reviewThread: TimelineReviewThread | undefined = undefined,
)}
  {#if event.Body}
    <div
      class={[
        "event-body-wrap",
        nested && "event-body-wrap--nested",
        !nested && reviewThread && "event-body-wrap--with-thread",
      ]}
    >
      {#if !nested && reviewThread}
        <DiffReviewThreadSnippet
          thread={reviewThread.thread}
          context={diff ? reviewThreadContext(diff, reviewThread.thread) : null}
          canResolve={reviewThread.thread.can_resolve && canResolveReviewThreads && diffReviewDraft != null}
          onchanged={refreshAfterThreadChange}
          jumpToDiff={jumpToReviewThread
            ? () => jumpToReviewThread(reviewThread.thread)
            : undefined}
        />
      {/if}
      <div class="event-actions">
        {#if canEditComment(event)}
          <button
            class="event-action-btn"
            onclick={() => startEdit(event)}
            title="Edit comment"
            aria-label="Edit comment"
            disabled={savingEditId !== null}
          >
            <PencilIcon size={14} />
          </button>
        {/if}
        <button
          class="event-action-btn"
          class:copied={copiedId === String(event.ID)}
          onclick={() => copyText(String(event.ID), event.Body)}
          title={copiedId === String(event.ID) ? "Copied!" : "Copy to clipboard"}
          aria-label={copiedId === String(event.ID) ? "Copied" : "Copy comment"}
        >
          {#if copiedId === String(event.ID)}
            <CheckIcon size={14} />
          {:else}
            <CopyIcon size={14} />
          {/if}
        </button>
      </div>
      {#if editingId === event.ID && provider && repoOwner && repoName && repoPath}
        <div class="edit-panel">
          <CommentEditor
            {provider}
            {platformHost}
            owner={repoOwner}
            name={repoName}
            {repoPath}
            value={editDraft}
            disabled={savingEditId === event.ID}
            oninput={(nextBody) => {
              editDraft = nextBody;
            }}
            onsubmit={() => {
              void saveEdit(event);
            }}
          />
          {#if editError}
            <p class="edit-error">{editError}</p>
          {/if}
          <div class="edit-actions">
            <button
              class="edit-action edit-action--primary"
              onclick={() => void saveEdit(event)}
              disabled={savingEditId === event.ID}
            >
              <CheckIcon size={14} />
              {savingEditId === event.ID ? "Saving..." : "Save"}
            </button>
            <button
              class="edit-action"
              onclick={cancelEdit}
              disabled={savingEditId === event.ID}
            >
              <XIcon size={14} />
              Cancel
            </button>
          </div>
        </div>
      {:else}
        <div class={["event-body", { "markdown-body": shouldRenderMarkdown(event.EventType), "event-body--nested": nested }]}>
          {#if shouldRenderMarkdown(event.EventType)}
            {@html renderMarkdown(event.Body, provider && repoOwner && repoName && repoPath ? { provider, platformHost, owner: repoOwner, name: repoName, repoPath } : undefined)}
          {:else}
            {event.Body}
          {/if}
        </div>
      {/if}
    </div>
  {:else if canEditComment(event)}
    <div class={nested ? "event-body-wrap event-body-wrap--nested" : "event-body-wrap"}>
      {#if editingId === event.ID && provider && repoOwner && repoName && repoPath}
        <div class="edit-panel">
          <CommentEditor
            {provider}
            {platformHost}
            owner={repoOwner}
            name={repoName}
            {repoPath}
            value={editDraft}
            disabled={savingEditId === event.ID}
            oninput={(nextBody) => {
              editDraft = nextBody;
            }}
            onsubmit={() => {
              void saveEdit(event);
            }}
          />
          {#if editError}
            <p class="edit-error">{editError}</p>
          {/if}
          <div class="edit-actions">
            <button
              class="edit-action edit-action--primary"
              onclick={() => void saveEdit(event)}
              disabled={savingEditId === event.ID}
            >
              <CheckIcon size={14} />
              {savingEditId === event.ID ? "Saving..." : "Save"}
            </button>
            <button
              class="edit-action"
              onclick={cancelEdit}
              disabled={savingEditId === event.ID}
            >
              <XIcon size={14} />
              Cancel
            </button>
          </div>
        </div>
      {:else}
        <button
          class="event-action-btn empty-edit-btn"
          onclick={() => startEdit(event)}
          title="Edit comment"
          aria-label="Edit comment"
          disabled={savingEditId !== null}
        >
          <PencilIcon size={14} />
        </button>
      {/if}
    </div>
  {/if}
{/snippet}

{#if events.length === 0}
  <p class="empty">{filtered ? "No activity matches the current filters" : "No activity yet"}</p>
{:else}
  <ol class="timeline">
    {#each timelineEntries as entry (entry.key)}
      {@const event = entry.event}
      {@const targetID = replyTargetID(entry)}
      <li class={isCompactEvent(event.EventType) ? "event event--compact" : "event"}>
        <div class="event-rail">
          <span
            class="dot"
            style="background: {typeColors[event.EventType] ?? 'var(--text-muted)'}"
          ></span>
          <span class="rail-line"></span>
        </div>
        {#if isCompactEvent(event.EventType)}
          {@const metadata = parseMetadata(event)}
          {@const commitDetails = event.EventType === "commit" ? commitDetailsBody(event.Body) : ""}
          <div class="event-card event-card--compact">
            <div class="event-header event-header--compact">
              {#if event.EventType !== "comment_deleted" && event.EventType !== "assigned" && event.EventType !== "unassigned"}
                <span
                  class="event-type"
                  style="color: {typeColors[event.EventType] ?? 'var(--text-muted)'}"
                >
                  {systemEventLabel(event.EventType)}
                </span>
              {/if}
              {#if event.EventType === "commit"}
                {#if event.Author}
                  <span class="event-author">{event.Author}</span>
                {/if}
                <span class="commit-sha">{shortCommit(event.Summary)}</span>
                {#if !showCommitDetails}
                  <span class="commit-title">{commitTitle(event.Body)}</span>
                {/if}
                <span class="event-time">{timeAgo(event.CreatedAt)}</span>
              {:else if event.EventType === "comment_deleted"}
                {#if event.Author}
                  <span class="event-author">{event.Author}</span>
                {/if}
                <span class="system-event-summary system-event-summary--sentence">{event.Summary}</span>
                <span class="event-time">{timeAgo(event.CreatedAt)}</span>
              {:else if event.EventType === "assigned" || event.EventType === "unassigned"}
                {#if event.Author}
                  <span class="event-author">{event.Author}</span>
                {/if}
                <span class="system-event-summary system-event-summary--sentence">{event.Summary}</span>
                <span class="event-time">{timeAgo(event.CreatedAt)}</span>
              {:else if event.EventType === "cross_referenced"}
                {#if event.Author}
                  <span class="event-author">{event.Author}</span>
                {/if}
                {@const sourceUrl = metadataString(metadata, "source_url")}
                {@const sourceTitle = metadataString(metadata, "source_title") ?? event.Summary}
                {@const sourceLink = crossReferenceLink(metadata, sourceUrl)}
                <span class="event-time">{timeAgo(event.CreatedAt)}</span>
                {#if sourceLink}
                  <a
                    class={["system-event-link", { "item-ref": sourceLink.internal }]}
                    href={sourceLink.href}
                    target={sourceLink.internal ? undefined : "_blank"}
                    rel={sourceLink.internal ? undefined : "noopener noreferrer"}
                    {...(sourceLink.dataAttributes ?? {})}
                  >
                    {sourceTitle}
                  </a>
                {:else}
                  <span class="system-event-summary">{sourceTitle}</span>
                {/if}
              {:else}
                {#if event.Author}
                  <span class="event-author">{event.Author}</span>
                {/if}
                <span class="event-time">{timeAgo(event.CreatedAt)}</span>
                <span class="system-event-summary">{event.Summary}</span>
              {/if}
            </div>
            {#if event.EventType === "commit" && showCommitDetails && commitDetails}
              <div
                class="event-body commit-body-details"
                transition:slide={{ duration: 100 }}
              >
                {commitDetails}
              </div>
            {/if}
          </div>
        {:else}
          <div class="event-card">
            <div class="event-header">
              <span
                class="event-type"
                style="color: {typeColors[event.EventType] ?? 'var(--text-muted)'}"
              >
                {typeLabels[event.EventType] ?? event.EventType}
              </span>
              {#if event.Author}
                <span class="event-author">{event.Author}</span>
              {/if}
              <span class="event-time">{timeAgo(event.CreatedAt)}</span>
            </div>
            {#if event.Summary && (event.EventType === "commit" || event.EventType === "force_push")}
              <p class="event-summary">{event.Summary}</p>
            {/if}
            {@render eventBody(event, false, entry.reviewThread)}
            {#if entry.replies.length > 0 || canReplyToThread(entry)}
              <div class="thread-controls">
                {#if entry.replies.length > 0}
                  <button
                    class="thread-toggle"
                    type="button"
                    onclick={() => toggleThread(entry)}
                    aria-expanded={!isThreadCollapsed(entry)}
                  >
                    {#if isThreadCollapsed(entry)}
                      <ChevronRightIcon size={14} />
                      Show {entry.replies.length} {entry.replies.length === 1 ? "reply" : "replies"}
                    {:else}
                      <ChevronDownIcon size={14} />
                      Hide {entry.replies.length} {entry.replies.length === 1 ? "reply" : "replies"}
                    {/if}
                  </button>
                {/if}
                {#if canReplyToThread(entry)}
                  <button
                    class="thread-toggle thread-reply-action"
                    type="button"
                    onclick={() => startReply(entry)}
                    aria-expanded={targetID !== null && replyingThreadID === targetID}
                    disabled={savingReplyThreadID !== null}
                  >
                    <MessageSquareReplyIcon size={14} />
                    Reply
                  </button>
                {/if}
              </div>
              {#if targetID !== null && replyingThreadID === targetID && provider && repoOwner && repoName && repoPath}
                <div class="thread-reply-panel">
                  <CommentEditor
                    {provider}
                    {platformHost}
                    owner={repoOwner}
                    name={repoName}
                    {repoPath}
                    value={replyDraft}
                    placeholder="Reply to thread... (Cmd+Enter to submit)"
                    disabled={savingReplyThreadID === targetID}
                    oninput={(nextBody) => {
                      replyDraft = nextBody;
                    }}
                    onsubmit={() => {
                      void submitReply(entry);
                    }}
                  />
                  {#if replyError}
                    <p class="edit-error">{replyError}</p>
                  {/if}
                  <div class="edit-actions">
                    <button
                      class="edit-action edit-action--primary"
                      onclick={() => void submitReply(entry)}
                      disabled={savingReplyThreadID === targetID}
                    >
                      <CheckIcon size={14} />
                      {savingReplyThreadID === targetID ? "Replying..." : "Reply"}
                    </button>
                    <button
                      class="edit-action"
                      onclick={cancelReply}
                      disabled={savingReplyThreadID === targetID}
                    >
                      <XIcon size={14} />
                      Cancel
                    </button>
                  </div>
                </div>
              {/if}
              {#if !isThreadCollapsed(entry)}
                <ol class="thread-replies" aria-label="Threaded replies">
                  {#each entry.replies as reply, index (reply.ID)}
                    <li
                      class="thread-reply"
                      class:thread-reply--first={index === 0}
                      class:thread-reply--last={index === entry.replies.length - 1}
                    >
                      <div class="thread-reply-rail" aria-hidden="true">
                        <span class="thread-reply-dot"></span>
                      </div>
                      <div class="thread-reply-content">
                        <div class="event-header thread-reply-header">
                          <span class="event-type">Reply</span>
                          {#if reply.Author}
                            <span class="event-author">{reply.Author}</span>
                          {/if}
                          <span class="event-time">{timeAgo(reply.CreatedAt)}</span>
                        </div>
                        {@render eventBody(reply, true)}
                      </div>
                    </li>
                  {/each}
                </ol>
              {/if}
            {/if}
          </div>
        {/if}
      </li>
    {/each}
  </ol>
{/if}

<style>
  .empty {
    font-size: var(--font-size-root);
    color: var(--text-muted);
    padding: 1.25rem 0;
  }

  .timeline {
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 0;
  }

  .event {
    display: flex;
    gap: 0;
  }

  /* Left rail: dot + connector line */
  .event-rail {
    display: flex;
    flex-direction: column;
    align-items: center;
    width: 1.85rem;
    flex-shrink: 0;
    padding-top: 1.08rem;
  }

  .dot {
    width: 0.77rem;
    height: 0.77rem;
    border-radius: 50%;
    flex-shrink: 0;
    z-index: 1;
    box-shadow: 0 0 0 0.23rem var(--bg-primary);
  }

  .rail-line {
    width: 0.15rem;
    flex: 1;
    background: var(--border-default);
    margin-top: 0.15rem;
  }

  .event:last-child .rail-line {
    display: none;
  }

  /* Right side: card */
  .event-card {
    flex: 1;
    min-width: 0;
    background: var(--bg-surface);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md);
    padding: var(--focus-detail-space-sm, 0.77rem) var(--focus-detail-space-sm, 0.92rem);
    margin: 0.31rem 0 0.31rem var(--focus-detail-space-xs, 0.62rem);
  }

  .event-card--compact {
    padding: var(--focus-detail-space-xs, 0.54rem) var(--focus-detail-space-sm, 0.77rem);
  }

  .event-header {
    display: flex;
    align-items: center;
    gap: var(--focus-detail-space-xs, 0.46rem);
    flex-wrap: wrap;
  }

  .event-header--compact {
    min-width: 0;
    flex-wrap: nowrap;
  }

  .event-header--compact .event-time {
    margin-left: 0;
  }

  .event-type {
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .event-author {
    font-size: var(--font-size-sm);
    font-weight: 500;
    color: var(--text-primary);
  }

  .event-time {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    margin-left: auto;
  }

  .event-summary {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    margin-top: var(--focus-detail-space-xs, 0.31rem);
    font-family: var(--font-mono);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .commit-sha {
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
  }

  .commit-title,
  .system-event-summary,
  .system-event-link {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .commit-title {
    flex: 1;
    color: var(--text-primary);
  }

  .commit-body-details {
    margin-top: var(--focus-detail-space-xs, 0.54rem);
    padding-right: var(--focus-detail-space-sm, 0.77rem);
  }

  .system-event-summary,
  .system-event-link {
    flex: 1;
    font-size: var(--font-size-sm);
  }

  .system-event-summary {
    color: var(--text-secondary);
  }

  .system-event-summary--sentence {
    flex: 0 1 auto;
  }

  .system-event-link {
    color: var(--accent-blue);
    text-decoration: none;
  }

  .system-event-link:hover {
    text-decoration: underline;
  }

  /* Body wrap for copy button positioning */
  .event-body-wrap {
    position: relative;
    margin-top: var(--focus-detail-space-sm, 0.62rem);
  }

  .event-body-wrap--nested {
    margin-top: 0.15rem;
  }

  .thread-controls {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--focus-detail-space-xs, 0.46rem);
    margin-top: var(--focus-detail-space-sm, 0.62rem);
  }

  .thread-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    min-height: 1.75rem;
    padding: 0.18rem 0.45rem 0.18rem 0.25rem;
    border-radius: var(--radius-sm);
    color: var(--accent-blue);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .thread-toggle:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .thread-toggle:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .thread-reply-action {
    color: var(--text-secondary);
  }

  .thread-reply-panel {
    padding: var(--focus-detail-space-sm, 0.62rem) 0 0.15rem 1.35rem;
  }

  .thread-replies {
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 0;
    margin-top: 0.2rem;
    padding-left: 0;
  }

  .thread-reply {
    display: grid;
    grid-template-columns: 1.35rem minmax(0, 1fr);
    column-gap: 0;
    min-width: 0;
    --thread-reply-header-padding-block: 0.18rem;
    --thread-reply-header-line-height: 1.15rem;
  }

  .thread-reply-rail {
    position: relative;
    min-height: 1.5rem;
    --thread-dot-size: 0.5rem;
    --thread-dot-center-y: calc(var(--thread-reply-header-padding-block) + 0.575rem);
  }

  .thread-reply-rail::before {
    content: "";
    position: absolute;
    top: 0;
    bottom: 0;
    left: calc(var(--thread-dot-size) / 2);
    width: 2px;
    background: var(--border-default);
    transform: translateX(-50%);
  }

  .thread-reply--first .thread-reply-rail::before {
    top: var(--thread-dot-center-y);
  }

  .thread-reply--last .thread-reply-rail::before {
    bottom: calc(100% - var(--thread-dot-center-y));
  }

  .thread-reply--first.thread-reply--last .thread-reply-rail::before {
    display: none;
  }

  .thread-reply-dot {
    position: absolute;
    top: calc(var(--thread-dot-center-y) - var(--thread-dot-size) / 2);
    left: 0;
    width: var(--thread-dot-size);
    height: var(--thread-dot-size);
    border-radius: 50%;
    background: var(--accent-blue);
    box-shadow: 0 0 0 0.18rem var(--bg-surface);
    z-index: 1;
  }

  .thread-reply-content {
    min-width: 0;
    padding: var(--thread-reply-header-padding-block) 0;
  }

  .thread-reply-header {
    min-width: 0;
    min-height: var(--thread-reply-header-line-height);
    align-items: center;
  }

  .thread-reply-header .event-type {
    color: var(--accent-blue);
  }

  .thread-reply-header .event-author {
    color: var(--text-secondary);
  }

  .event-actions {
    position: absolute;
    top: var(--focus-detail-space-xs, 0.46rem);
    right: var(--focus-detail-space-xs, 0.46rem);
    display: flex;
    gap: 0.15rem;
    z-index: 1;
  }

  .event-body-wrap--with-thread .event-actions {
    position: static;
    justify-content: flex-end;
    margin-top: calc(var(--focus-detail-space-xs, 0.46rem) * -1);
    margin-bottom: var(--focus-detail-space-xs, 0.46rem);
  }

  .event-action-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: var(--focus-detail-hit-target, 2rem);
    height: var(--focus-detail-hit-target, 2rem);
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    opacity: 0;
    transition: opacity 0.15s, background 0.15s, color 0.15s;
  }

  .event-body-wrap:hover .event-action-btn,
  .event-action-btn:focus-visible {
    opacity: 1;
  }

  .event-action-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-secondary);
  }

  .event-action-btn:active:not(:disabled) {
    transform: scale(0.92);
  }

  .event-action-btn.copied {
    opacity: 1;
    color: var(--accent-green);
    background: color-mix(in srgb, var(--accent-green) 12%, transparent);
  }

  .event-action-btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  @media (hover: none) {
    .event-action-btn {
      opacity: 1;
    }
  }

  .event-body {
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    padding: var(--focus-detail-space-sm, 0.62rem) calc(var(--focus-detail-hit-target, 2rem) + var(--focus-detail-space-sm, 0.62rem)) var(--focus-detail-space-sm, 0.62rem) var(--focus-detail-space-sm, 0.77rem);
    white-space: pre-wrap;
    word-break: break-word;
    line-height: 1.6;
  }

  .event-body.markdown-body {
    white-space: normal;
  }

  .event-body--nested {
    padding: 0.12rem calc(var(--focus-detail-hit-target, 2rem) + var(--focus-detail-space-sm, 0.62rem)) 0.15rem 0;
    line-height: 1.25;
  }

  .edit-panel {
    padding: var(--focus-detail-space-sm, 0.62rem) 0 0.15rem;
  }

  .edit-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--focus-detail-space-sm, 0.62rem);
    margin-top: var(--focus-detail-space-sm, 0.62rem);
  }

  .edit-action {
    display: inline-flex;
    align-items: center;
    gap: 0.38rem;
    min-height: var(--focus-detail-hit-target, 2.15rem);
    padding: 0.38rem var(--focus-detail-space-sm, 0.77rem);
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-inset);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .edit-action--primary {
    border-color: var(--accent-blue);
    background: var(--accent-blue);
    color: white;
  }

  .edit-action:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .edit-action:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .edit-action--primary:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-blue) 86%, black);
    color: white;
  }

  .edit-error {
    margin-top: var(--focus-detail-space-xs, 0.46rem);
    font-size: var(--font-size-sm);
    color: var(--accent-red);
  }

  .empty-edit-btn {
    position: static;
    opacity: 1;
    margin-top: var(--focus-detail-space-sm, 0.62rem);
  }
</style>
