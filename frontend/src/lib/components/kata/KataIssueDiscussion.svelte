<script lang="ts">
  import { MentionTextarea, type MentionOption } from "@kenn-io/kit-ui";
  import { Button, ItemStateChip } from "@middleman/ui";
  import { renderMarkdown, renderMarkdownSync } from "@middleman/ui/utils/markdown";
  import { localDateTimeLabel, timeAgo } from "@middleman/ui/utils/time";

  import type {
    KataTaskAPI,
    KataTaskDetail,
    KataTaskEditPatch,
    KataTaskEvent,
    KataTaskGroup,
    KataTaskLink,
    KataTaskSummary,
  } from "../../api/kata/taskTypes.js";
  import { describeKataEvent } from "../../features/kata/eventFormatter";
  import {
    kataLinkCouldAffectVisibleResults,
    kataLinkMatchesFilters,
    relationForKataLink,
    type KataLinkFilters,
    type KataLinkPeerResolution,
    type KataLinkRelation,
  } from "../../features/kata/kataLinkFilters.js";
  import KataLinkFilterMenu from "./KataLinkFilterMenu.svelte";

  interface Props {
    issue: KataTaskDetail;
    events: KataTaskEvent[];
    currentView: { groups: KataTaskGroup[] };
    api: KataTaskAPI;
    activeDaemonId?: string | undefined;
    linkFilters: KataLinkFilters;
    onLinkFiltersChange: (next: KataLinkFilters) => void;
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
    linkFilters,
    onLinkFiltersChange,
    onAddComment,
    onEditIssue,
    onSelectIssue,
  }: Props = $props();

  let commentDraft = $state("");
  let relatedDraft = $state("");
  let hydratedPeers = $state<Record<string, KataTaskSummary | null>>({});
  let peerHydrationSignature = $state("");
  let pendingPeerKeys = $state<ReadonlySet<string>>(new Set());
  let lastSelectedDetail: KataTaskDetail | null = null;

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
    const current = issue;
    if (lastSelectedDetail !== null && current !== lastSelectedDetail && current.issue.uid === lastSelectedDetail.issue.uid) {
      hydratedPeers = Object.fromEntries(Object.entries(hydratedPeers).filter(([, peer]) => peer !== null));
    }
    lastSelectedDetail = current;
  });

  $effect(() => {
    const signature = linkHydrationSignature();
    if (signature !== peerHydrationSignature) {
      peerHydrationSignature = signature;
      hydratedPeers = {};
      pendingPeerKeys = new Set();
    }
    const peerUIDs = issue.links
      .map((link) => linkPeerUIDFor(link, issue.issue.uid))
      .filter(
        (uid) =>
          uid &&
          currentViewPeer(uid) === undefined &&
          hydratedPeers[uid] === undefined &&
          !pendingPeerKeys.has(`${signature}:${uid}`),
      );
    if (peerUIDs.length === 0) return;
    for (const uid of new Set(peerUIDs)) {
      const key = `${signature}:${uid}`;
      pendingPeerKeys = new Set([...pendingPeerKeys, key]);
      void api
        .issue(uid)
        .then((detail) => {
          if (signature !== linkHydrationSignature()) return;
          hydratedPeers = { ...hydratedPeers, [uid]: detail.issue };
        })
        .catch(() => {
          if (signature !== linkHydrationSignature()) return;
          hydratedPeers = { ...hydratedPeers, [uid]: null };
        })
        .finally(() => {
          pendingPeerKeys = new Set([...pendingPeerKeys].filter((candidate) => candidate !== key));
        });
    }
  });

  const visibleLinks = $derived(
    issue.links.filter((link) =>
      kataLinkMatchesFilters(link, issue.issue.uid, peerResolution(link), linkFilters),
    ),
  );
  const showStateChips = $derived(linkFilters.statuses.open && linkFilters.statuses.closed);
  const unresolvedPeerCount = $derived(
    issue.links.filter(
      (link) =>
        peerResolution(link).kind === "pending" &&
        kataLinkCouldAffectVisibleResults(link, issue.issue.uid, linkFilters),
    ).length,
  );

  async function submitComment(): Promise<void> {
    const body = commentDraft.trim();
    if (!body) return;
    const ok = await onAddComment(issue.issue.uid, body);
    if (ok) {
      commentDraft = "";
    }
  }

  function currentViewPeer(uid: string): KataTaskSummary | undefined {
    for (const group of currentView.groups) {
      const found = group.issues.find((candidate) => candidate.uid === uid);
      if (found) return found;
    }
    return undefined;
  }

  function linkHydrationSignature(): string {
    const links = issue.links.map((link) => `${link.id}:${linkPeerUIDFor(link, issue.issue.uid)}:${link.type}`).join("|");
    return `${activeDaemonId ?? ""}:${issue.issue.uid}:${links}`;
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

  function peerResolution(link: KataTaskLink): KataLinkPeerResolution {
    const uid = linkPeerUID(link);
    const current = currentViewPeer(uid);
    if (current) return { kind: "resolved", peer: current };
    const hydrated = hydratedPeers[uid];
    if (hydrated === null) return { kind: "failed" };
    if (hydrated !== undefined) return { kind: "resolved", peer: hydrated };
    return { kind: "pending" };
  }

  const relationLabels: Record<KataLinkRelation, string> = {
    parent: "parent",
    child: "child",
    blocks: "blocks",
    blocked_by: "blocked_by",
    related: "related",
  };

  function linkLabel(link: KataTaskLink): string {
    return relationLabels[relationForKataLink(link, issue.issue.uid)];
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

  async function searchTaskReferences(query: string): Promise<MentionOption[]> {
    const response = await api.search({
      scope: { kind: "all" },
      status: "open",
      owner: "",
      label: "",
      query,
    });
    const shortIDCounts = new Map<string, number>();
    for (const task of response.issues) {
      shortIDCounts.set(task.short_id, (shortIDCounts.get(task.short_id) ?? 0) + 1);
    }
    return response.issues.map((task) => ({
      id: task.uid,
      insert: shortIDCounts.get(task.short_id) === 1 ? task.short_id : task.qualified_id,
      label: task.title,
      meta: task.project_name,
    }));
  }
</script>

<section class="task-links" aria-label="Links">
  <div class="section-header link-section-header">
    <h3>Links</h3>
    <div class="link-header-actions">
      <span>
        {visibleLinks.length === issue.links.length
          ? issue.links.length
          : `${visibleLinks.length} / ${issue.links.length}`}
      </span>
      {#if unresolvedPeerCount > 0}<span class="link-loading">Resolving {unresolvedPeerCount}</span>{/if}
      <KataLinkFilterMenu filters={linkFilters} onChange={onLinkFiltersChange} />
    </div>
  </div>
  {#if issue.links.length === 0}
    <p class="link-empty">No links.</p>
  {:else if visibleLinks.length === 0 && unresolvedPeerCount > 0}
    <p class="link-empty">Resolving linked tasks...</p>
  {:else if visibleLinks.length === 0}
    <p class="link-empty">No links match these filters.</p>
  {:else}
    <div class="link-list" aria-busy={unresolvedPeerCount > 0}>
      {#each visibleLinks as link (link.id)}
        {@const resolution = peerResolution(link)}
        {@const peer = resolution.kind === "resolved" ? resolution.peer : undefined}
        <button
          type="button"
          class={["link-row", (showStateChips || resolution.kind === "failed") && "link-row--with-state"]}
          aria-label={`${linkLabel(link)} ${linkPeerShortID(link)} ${peer?.title ?? ""}${showStateChips && peer ? ` ${peer.status}` : ""}${resolution.kind === "failed" ? " state unavailable" : ""}`.trim()}
          onclick={() => {
            void onSelectIssue(linkPeerUID(link));
          }}
        >
          <span class="link-kind">{linkLabel(link)}</span>
          <span class="link-peer">{linkPeerShortID(link)}</span>
          {#if peer?.title}<span class="link-title">{peer.title}</span>{/if}
          {#if showStateChips && peer}
            <ItemStateChip state={peer.status} size="xs" />
          {:else if resolution.kind === "failed"}
            <ItemStateChip state="unknown" size="xs" title="Task state unavailable" />
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
    <Button
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
    <MentionTextarea
      ariaLabel="Comment"
      rows={3}
      bind:value={commentDraft}
      search={searchTaskReferences}
      emptyLabel="No matching tasks"
      placeholder="Add a comment..."
    />
    <Button
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
          <div class="comment-meta">
            <span>{comment.author}</span>
            <time datetime={comment.created_at} title={localDateTimeLabel(comment.created_at)}>
              {timeAgo(comment.created_at)}
            </time>
          </div>
          <div class="comment-body markdown-body">
            {#await renderMarkdown(comment.body)}
              {@html renderMarkdownSync(comment.body)}
            {:then html}
              {@html html}
            {/await}
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

  .link-header-actions {
    display: inline-flex;
    align-items: center;
    gap: var(--space-3);
  }

  .link-header-actions > span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .link-header-actions > .link-loading {
    color: var(--text-faint);
  }

  .link-empty,
  .events p {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .link-list {
    display: grid;
    gap: var(--space-1);
  }

  .link-row {
    width: 100%;
    min-height: 32px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: var(--text-primary);
    display: grid;
    grid-template-columns: max-content max-content minmax(0, 1fr);
    align-items: center;
    gap: 8px;
    padding: 4px 6px;
    font: inherit;
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
  }

  .link-row--with-state {
    grid-template-columns: max-content max-content minmax(0, 1fr) max-content;
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
    gap: var(--space-1);
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
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    margin-bottom: 4px;
  }

  .comment-meta time {
    flex: 0 0 auto;
    white-space: nowrap;
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
