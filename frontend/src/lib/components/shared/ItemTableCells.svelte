<script lang="ts">
  // The Repo, Author, Item, Status, When, and link cells of one item table row. The row element
  // and any leading cells belong to the caller, so each list keeps its own selection and markup.
  import ArrowUpRightIcon from "@lucide/svelte/icons/arrow-up-right";
  import { Chip, hashColor } from "@kenn-io/kit-ui";
  import type { Snippet } from "svelte";
  import AgentStatusIndicator from "./AgentStatusIndicator.svelte";
  import ItemStateChip from "./ItemStateChip.svelte";
  import RepoPrivateIcon from "./RepoPrivateIcon.svelte";
  import WorkspaceIndicator from "./WorkspaceIndicator.svelte";

  interface Props {
    /** Omitted when the table has no Repo column. */
    repo?: { label: string; colorKey: string; isPrivate: boolean | undefined; title?: string } | undefined;
    /** Omitted when the table has no Author column. */
    author?: string | undefined;
    /** A state worth a chip before the reference, such as "merged". */
    state?: string | undefined;
    /** The item number or branch name. */
    reference: string;
    workspaceStatus?: string | undefined;
    title: string;
    agentState?: string | undefined;
    /** Between the reference and the title, such as a commit SHA. */
    beforeTitle?: Snippet | undefined;
    /** After the title, such as labels, in the room the title leaves. */
    afterTitle?: Snippet | undefined;
    /** Fills the Status column; the table must declare one. */
    status?: Snippet | undefined;
    /** Omitted, with its title and label, when the table has no When column. */
    time?: string | undefined;
    timeTitle?: string | undefined;
    timeLabel?: string | undefined;
    linkUrl?: string | undefined;
    linkLabel: string;
    linkTitle: string;
    /** Controls before the link, such as marking a notification seen. */
    actions?: Snippet | undefined;
    /** The row is a button, which cannot hold another button. */
    insideButton?: boolean;
  }

  const {
    repo,
    author,
    state,
    reference,
    workspaceStatus,
    title,
    agentState,
    beforeTitle,
    afterTitle,
    status,
    time,
    timeTitle,
    timeLabel,
    linkUrl,
    linkLabel,
    linkTitle,
    actions,
    insideButton = false,
  }: Props = $props();

  function openLink(event: Event): void {
    event.stopPropagation();
    if (linkUrl) window.open(linkUrl, "_blank", "noopener");
  }

  function openLinkFromKey(event: KeyboardEvent): void {
    if (!insideButton || (event.key !== "Enter" && event.key !== " ")) return;
    event.preventDefault();
    openLink(event);
  }
</script>

{#if repo}
  {@const color = hashColor(repo.colorKey)}
  <span class="cell cell--repo col-repo">
    <Chip
      size="sm"
      uppercase={false}
      class="repo-chip repo-tag"
      style={`color: ${color}; background: color-mix(in srgb, ${color} 15%, transparent);`}
    >
      <span class="repo-chip__label" title={repo.title}>{repo.label}</span>
    </Chip>
    <RepoPrivateIcon isPrivate={repo.isPrivate} />
  </span>
{/if}
{#if author !== undefined}
  <span class="cell cell--author col-author">{author}</span>
{/if}
<span class="cell cell--title">
  {#if state}
    <ItemStateChip {state} />
  {/if}
  <span class="item-ref">{reference}</span>
  {@render beforeTitle?.()}
  {#if workspaceStatus !== undefined}
    <WorkspaceIndicator status={workspaceStatus} size={12} />
  {/if}
  <span class={["item-title", afterTitle && "item-title--reserved"]}>{title}</span>
  {@render afterTitle?.()}
  <AgentStatusIndicator state={agentState} />
</span>
{#if status}
  <span class="cell cell--status">{@render status()}</span>
{/if}
{#if time !== undefined}
  <span class="cell cell--time col-when" title={timeTitle} aria-label={timeLabel}>{time}</span>
{/if}
<span class="cell cell--link">
  {@render actions?.()}
  {#if linkUrl}
    <svelte:element
      this={insideButton ? "span" : "button"}
      class="link-btn"
      type={insideButton ? undefined : "button"}
      role={insideButton ? "button" : undefined}
      tabindex={insideButton ? -1 : undefined}
      aria-label={linkLabel}
      title={linkTitle}
      onclick={openLink}
      onkeydown={openLinkFromKey}
    >
      <ArrowUpRightIcon size="14" strokeWidth="2" aria-hidden="true" />
    </svelte:element>
  {/if}
</span>

<style>
  .cell {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .cell--repo {
    display: inline-flex;
    align-items: center;
  }

  .cell--repo :global(.repo-chip) {
    min-width: 0;
    max-width: 100%;
  }

  .repo-chip__label {
    display: block;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .cell--author {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }

  /* A stack member indents its item, leaving the other columns in line. */
  .cell--title {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding-inline-start: var(--item-table-indent, 0px);
  }

  .item-ref {
    font-size: var(--font-size-sm);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .item-title {
    font-size: var(--font-size-sm);
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }

  /* LabelRow reads flex-basis as the width the title keeps, up to its text. A basis
     wider than any title leaves the labels only the room the title does not need. */
  .item-title--reserved {
    flex: 1 1 60rem;
    max-width: max-content;
  }

  /* Never squeezed: the title gives way instead. */
  .cell--status {
    display: inline-flex;
    align-items: center;
    justify-content: flex-end;
    gap: 6px;
    min-width: auto;
    overflow: visible;
  }

  .cell--time {
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    text-align: right;
  }

  .cell--link {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    overflow: visible;
  }

  .link-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: var(--text-muted);
    background: none;
    border: 0;
    padding: 2px;
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition: color 0.1s, background 0.1s;
  }

  .link-btn:hover {
    color: var(--accent-blue);
    background: var(--bg-surface-hover);
  }

  .link-btn:focus-visible {
    outline: 2px solid var(--accent-blue);
    outline-offset: 1px;
  }
</style>
