<script lang="ts">
  import CheckIcon from "@lucide/svelte/icons/check";
  import CopyIcon from "@lucide/svelte/icons/copy";
  import PencilIcon from "@lucide/svelte/icons/pencil";
  import XIcon from "@lucide/svelte/icons/x";
  import { slide } from "svelte/transition";
  import type { IssueEvent, PREvent } from "../../api/types.js";
  import { renderMarkdown } from "../../utils/markdown.js";
  import { timeAgo } from "../../utils/time.js";
  import { copyToClipboard } from "../../utils/clipboard.js";
  import {
    buildItemReferenceLink,
    type ItemReferenceDataAttributes,
  } from "../../utils/item-reference.js";
  import CommentEditor from "./CommentEditor.svelte";

  interface Props {
    events: Array<PREvent | IssueEvent>;
    provider?: string | undefined;
    platformHost?: string | undefined;
    repoOwner?: string;
    repoName?: string;
    repoPath?: string | undefined;
    filtered?: boolean;
    showCommitDetails?: boolean;
    onEditComment?: ((event: PREvent | IssueEvent, body: string) => Promise<boolean>) | undefined;
  }

  const {
    events,
    provider,
    platformHost,
    repoOwner,
    repoName,
    repoPath,
    filtered = false,
    showCommitDetails = true,
    onEditComment,
  }: Props = $props();

  const typeLabels: Record<string, string> = {
    issue_comment: "Comment",
    review: "Review",
    commit: "Commit",
    force_push: "Force-pushed",
    review_comment: "Review Comment",
  };

  const typeColors: Record<string, string> = {
    issue_comment: "var(--accent-blue)",
    review: "var(--accent-purple)",
    review_comment: "var(--accent-purple)",
    commit: "var(--accent-green)",
    force_push: "var(--accent-red)",
  };

  function shouldRenderMarkdown(eventType: string): boolean {
    return eventType === "issue_comment" || eventType === "review" || eventType === "review_comment";
  }

  function isCompactEvent(eventType: string): boolean {
    return (
      eventType === "commit" ||
      eventType === "force_push" ||
      eventType === "cross_referenced" ||
      eventType === "renamed_title" ||
      eventType === "base_ref_changed"
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
      case "renamed_title":
        return "Title changed";
      case "base_ref_changed":
        return "Base changed";
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

  let copiedId = $state<string | null>(null);
  let copyTimeout: ReturnType<typeof setTimeout> | null = null;
  let editingId = $state<number | null>(null);
  let editDraft = $state("");
  let savingEditId = $state<number | null>(null);
  let editError = $state<string | null>(null);

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

{#if events.length === 0}
  <p class="empty">{filtered ? "No activity matches the current filters" : "No activity yet"}</p>
{:else}
  <ol class="timeline">
    {#each events as event (event.ID)}
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
              <span
                class="event-type"
                style="color: {typeColors[event.EventType] ?? 'var(--text-muted)'}"
              >
                {systemEventLabel(event.EventType)}
              </span>
              {#if event.Author}
                <span class="event-author">{event.Author}</span>
              {/if}
              {#if event.EventType === "commit"}
                <span class="commit-sha">{shortCommit(event.Summary)}</span>
                {#if !showCommitDetails}
                  <span class="commit-title">{commitTitle(event.Body)}</span>
                {/if}
                <span class="event-time">{timeAgo(event.CreatedAt)}</span>
              {:else if event.EventType === "cross_referenced"}
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
            {#if event.Body}
              <div class="event-body-wrap">
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
                  <div class="event-body {shouldRenderMarkdown(event.EventType) ? 'markdown-body' : ''}">
                    {#if shouldRenderMarkdown(event.EventType)}
                      {@html renderMarkdown(event.Body, provider && repoOwner && repoName && repoPath ? { provider, platformHost, owner: repoOwner, name: repoName, repoPath } : undefined)}
                    {:else}
                      {event.Body}
                    {/if}
                  </div>
                {/if}
              </div>
            {:else if canEditComment(event)}
              <div class="event-body-wrap">
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

  .event-actions {
    position: absolute;
    top: var(--focus-detail-space-xs, 0.46rem);
    right: var(--focus-detail-space-xs, 0.46rem);
    display: flex;
    gap: 0.15rem;
    z-index: 1;
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
