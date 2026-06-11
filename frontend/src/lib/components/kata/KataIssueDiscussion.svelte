<script lang="ts">
  import { ActionButton } from "@middleman/ui";
  import { renderMarkdown } from "@middleman/ui/utils/markdown";

  import type {
    KataTaskAPI,
    KataTaskDetail,
    KataTaskEditPatch,
    KataTaskEvent,
    KataTaskGroup,
    KataTaskLink,
  } from "../../api/kata/taskTypes.js";
  import { describeKataEvent } from "../../features/kata/eventFormatter";
  import TaskReferenceTextarea from "../shared/TaskReferenceTextarea.svelte";

  interface Props {
    issue: KataTaskDetail;
    events: KataTaskEvent[];
    currentView: { groups: KataTaskGroup[] };
    api: KataTaskAPI;
    activeDaemonId?: string | undefined;
    onAddComment: (uid: string, body: string) => boolean | Promise<boolean>;
    onEditIssue: (uid: string, patch: KataTaskEditPatch) => boolean | Promise<boolean>;
    onSelectIssue: (uid: string) => void | Promise<void>;
  }

  let {
    issue,
    events,
    currentView,
    api,
    activeDaemonId = undefined,
    onAddComment,
    onEditIssue,
    onSelectIssue,
  }: Props = $props();

  let commentDraft = $state("");
  let relatedDraft = $state("");
  let linkTitles = $state<Record<string, string>>({});
  let linkTitleSignature = $state("");
  let pendingLinkTitleKeys = $state<ReadonlySet<string>>(new Set());

  const sortedComments = $derived.by(() => {
    const comments = issue.comments ?? [];
    return [...comments].sort((a, b) => {
      const ta = Date.parse(a.created_at);
      const tb = Date.parse(b.created_at);
      if (Number.isNaN(ta) || Number.isNaN(tb)) return 0;
      return tb - ta;
    });
  });

  $effect(() => {
    const signature = linkHydrationSignature();
    if (signature !== linkTitleSignature) {
      linkTitleSignature = signature;
      linkTitles = {};
      pendingLinkTitleKeys = new Set();
    }
    const peerUIDs = issue.links
      .map((link) => linkPeerUIDFor(link, issue.issue.uid))
      .filter((uid) => uid && linkTitles[uid] === undefined && !pendingLinkTitleKeys.has(`${signature}:${uid}`));
    if (peerUIDs.length === 0) return;
    for (const uid of new Set(peerUIDs)) {
      const key = `${signature}:${uid}`;
      pendingLinkTitleKeys = new Set([...pendingLinkTitleKeys, key]);
      void api
        .issue(uid)
        .then((detail) => {
          if (signature !== linkHydrationSignature()) return;
          linkTitles = { ...linkTitles, [uid]: detail.issue.title };
        })
        .catch(() => {
          if (signature !== linkHydrationSignature()) return;
          linkTitles = { ...linkTitles, [uid]: "" };
        })
        .finally(() => {
          pendingLinkTitleKeys = new Set([...pendingLinkTitleKeys].filter((candidate) => candidate !== key));
        });
    }
  });

  async function submitComment(): Promise<void> {
    const body = commentDraft.trim();
    if (!body) return;
    const ok = await onAddComment(issue.issue.uid, body);
    if (ok) {
      commentDraft = "";
    }
  }

  function currentIssueTitle(uid: string): string {
    if (issue.issue.uid === uid) return issue.issue.title;
    for (const group of currentView.groups) {
      const found = group.issues.find((item) => item.uid === uid);
      if (found) return found.title;
    }
    return linkTitles[uid] ?? "";
  }

  function linkHydrationSignature(): string {
    const links = issue.links.map((link) => `${link.id}:${linkPeerUIDFor(link, issue.issue.uid)}:${link.type}`).join("|");
    return `${activeDaemonId ?? ""}:${issue.issue.uid}:${issue.issue.revision}:${links}`;
  }

  function linkPeerUIDFor(link: KataTaskLink, selectedUID: string | undefined): string {
    return link.from.uid === selectedUID ? link.to.uid : link.from.uid;
  }

  function linkPeerUID(link: KataTaskLink): string {
    return linkPeerUIDFor(link, issue.issue.uid);
  }

  function linkPeerShortID(link: KataTaskLink): string {
    return link.from.uid === issue.issue.uid ? link.to.short_id : link.from.short_id;
  }

  function linkPeerTitle(link: KataTaskLink): string {
    return currentIssueTitle(linkPeerUID(link));
  }

  function linkLabel(link: KataTaskLink): string {
    if (link.type === "blocks" && link.to.uid === issue.issue.uid) return "blocked_by";
    if (link.type === "parent") return link.to.uid === issue.issue.uid ? "parent" : "child";
    return link.type;
  }

  async function submitRelatedLink(): Promise<void> {
    const ref = relatedDraft.trim();
    if (ref === "") return;
    const ok = await onEditIssue(issue.issue.uid, {
      links_delta: { add_related: [ref] },
    });
    if (ok) {
      relatedDraft = "";
    }
  }

  function handleRelatedKeydown(event: KeyboardEvent): void {
    if (event.key === "Enter") {
      event.preventDefault();
      void submitRelatedLink();
    }
  }
</script>

<section class="task-links" aria-label="Links">
  <div class="section-header">
    <h3>Links</h3>
    <span>{issue.links.length}</span>
  </div>
  {#if issue.links.length === 0}
    <p class="link-empty">No links.</p>
  {:else}
    <div class="link-list">
      {#each issue.links as link (link.id)}
        <button
          type="button"
          class="link-row"
          onclick={() => {
            void onSelectIssue(linkPeerUID(link));
          }}
        >
          <span class="link-kind">{linkLabel(link)}</span>
          <span class="link-peer">{linkPeerShortID(link)}</span>
          {#if linkPeerTitle(link)}
            <span class="link-title">{linkPeerTitle(link)}</span>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
  <form
    class="link-form"
    onsubmit={(event) => {
      event.preventDefault();
      void submitRelatedLink();
    }}
  >
    <label>
      <span>Related issue</span>
      <input
        aria-label="Related issue"
        placeholder="Short id"
        bind:value={relatedDraft}
        onkeydown={handleRelatedKeydown}
      />
    </label>
    <ActionButton
      type="submit"
      surface="outline"
      size="sm"
      label="Link"
      disabled={relatedDraft.trim() === ""}
    />
  </form>
</section>

<section class="comments" aria-labelledby="kata-comments-title">
  <h3 id="kata-comments-title">Comments</h3>
  <form
    class="comment-composer"
    onsubmit={(event) => {
      event.preventDefault();
      void submitComment();
    }}
  >
    <TaskReferenceTextarea
      ariaLabel="Comment"
      rows={3}
      bind:value={commentDraft}
      {api}
      placeholder="Add a comment..."
    />
    <ActionButton
      type="submit"
      tone="info"
      surface="solid"
      size="sm"
      class="comment-submit"
      label="Add comment"
      disabled={commentDraft.trim() === ""}
    />
  </form>
  {#if sortedComments.length === 0}
    <p>No comments</p>
  {:else}
    <div class="comment-list">
      {#each sortedComments as comment (comment.id)}
        <article class="comment">
          <div class="comment-meta">{comment.author}</div>
          <div class="comment-body markdown-body">
            {@html renderMarkdown(comment.body)}
          </div>
        </article>
      {/each}
    </div>
  {/if}
</section>

<section class="events" aria-labelledby="kata-events-title">
  <h3 id="kata-events-title">Events</h3>
  {#if events.length === 0}
    <p>No events</p>
  {:else}
    <ul>
      {#each events as event (event.event_uid)}
        {@const descriptor = describeKataEvent(event)}
        {@const EventIcon = descriptor.icon}
        <li class="event-row" data-tone={descriptor.tone}>
          <span class="event-icon" aria-hidden="true">
            <EventIcon size={14} strokeWidth={1.8} />
          </span>
          <span>{descriptor.label}</span>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .task-links {
    display: grid;
    gap: 8px;
    margin: 0 0 18px;
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 8px;
  }

  .section-header h3,
  .comments h3,
  .events h3 {
    margin: 0 0 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
    text-transform: uppercase;
  }

  .section-header h3 {
    margin: 0;
  }

  .section-header > span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .link-empty,
  .events p {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .link-list {
    display: grid;
    gap: 3px;
  }

  .link-row {
    width: 100%;
    min-height: 32px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text-primary);
    display: grid;
    grid-template-columns: minmax(72px, max-content) minmax(54px, max-content) minmax(0, 1fr);
    align-items: center;
    gap: 8px;
    padding: 4px 6px;
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
  }

  .link-row:hover {
    background: var(--bg-hover);
  }

  .link-kind {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
  }

  .link-peer {
    color: var(--text-primary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .link-title {
    min-width: 0;
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .link-form {
    display: flex;
    align-items: flex-end;
    gap: 6px;
  }

  .link-form label {
    min-width: 0;
    flex: 1;
    display: grid;
    gap: 3px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
  }

  .link-form input {
    width: 100%;
    min-height: 28px;
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    font-weight: 500;
    padding: 4px 8px;
  }

  .comments {
    margin: 0 0 22px;
  }

  .comment-composer {
    display: grid;
    gap: 8px;
    margin-bottom: 12px;
  }

  .comment-composer :global(.comment-submit) {
    justify-self: end;
  }

  .comment-list {
    display: grid;
    gap: 8px;
  }

  .comment {
    border: 1px solid var(--border-default);
    border-radius: 6px;
    background: var(--bg-secondary);
    padding: 8px 10px;
  }

  .comment-meta {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    margin-bottom: 4px;
  }

  .comment-body :global(p) {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.45;
    white-space: pre-wrap;
  }

  .events ul {
    margin: 0;
    padding: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    list-style: none;
  }

  .event-row {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 24px;
  }

  .event-icon {
    flex: 0 0 auto;
    display: inline-flex;
    color: var(--text-muted);
  }

  .event-row[data-tone="positive"] .event-icon {
    color: var(--accent-green);
  }

  .event-row[data-tone="negative"] .event-icon {
    color: var(--accent-red);
  }

  .event-row[data-tone="warning"] .event-icon {
    color: var(--accent-amber);
  }
</style>
