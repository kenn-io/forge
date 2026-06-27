<script lang="ts">
  import type { CommitInfo } from "../../api/types.js";
  import {
    localDateLabel,
    parseAPITimestamp,
  } from "../../utils/time.js";

  interface Props {
    commit: CommitInfo;
    active: boolean;
    onclick: (sha: string, shiftKey: boolean) => void;
  }

  const { commit, active, onclick }: Props = $props();

  function relativeDate(iso: string): string {
    const diff = Date.now() - parseAPITimestamp(iso).getTime();
    const mins = Math.floor(diff / 60000);
    if (mins < 1) return "now";
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    if (days < 30) return `${days}d ago`;
    return localDateLabel(iso);
  }

  function handleClick(e: MouseEvent): void {
    onclick(commit.sha, e.shiftKey);
  }
</script>

<button
  type="button"
  class="commit-item"
  class:commit-item--active={active}
  onclick={handleClick}
  title={commit.message}
>
  <span
    class="commit-item__push"
    class:commit-item__push--unpushed={commit.pushed === false}
    role={commit.pushed === false ? "img" : null}
    aria-label={commit.pushed === false ? "Not pushed to remote" : null}
    aria-hidden={commit.pushed === false ? null : "true"}
    title={commit.pushed === false ? "Not pushed to remote" : null}
  ></span>
  <span class="commit-item__sha">{commit.sha.slice(0, 7)}</span>
  <span class="commit-item__msg">{commit.message}</span>
  <span class="commit-item__date">{relativeDate(commit.authored_at)}</span>
</button>

<style>
  .commit-item {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 3px 10px 3px 12px;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    line-height: 1.4;
    cursor: pointer;
    text-align: left;
    border: none;
    background: none;
  }

  .commit-item:hover {
    background: var(--bg-surface-hover);
  }

  .commit-item--active {
    background: var(--diff-sidebar-active);
    color: var(--text-primary);
  }

  .commit-item__push {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
    background: transparent;
  }

  .commit-item__push--unpushed {
    background: var(--accent-amber);
  }

  .commit-item__sha {
    font-family: var(--font-mono);
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    width: 52px;
    flex-shrink: 0;
  }

  .commit-item--active .commit-item__sha {
    color: var(--accent-blue);
  }

  .commit-item__msg {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }

  .commit-item__date {
    font-size: var(--font-size-2xs);
    color: var(--text-muted);
    flex-shrink: 0;
    margin-left: auto;
  }
</style>
