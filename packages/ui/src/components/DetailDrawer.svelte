<script lang="ts">
  import { getStackDepth } from "../stores/keyboard/modal-stack.svelte.js";
  import PullDetail from "./detail/PullDetail.svelte";
  import IssueDetail from "./detail/IssueDetail.svelte";

  interface Props {
    itemType: "pr" | "issue";
    provider: string;
    platformHost?: string | undefined;
    owner: string;
    name: string;
    repoPath: string;
    number: number;
    onClose: () => void;
    onPullsRefresh?: () => Promise<void>;
  }

  let {
    itemType,
    provider,
    platformHost,
    owner,
    name,
    repoPath,
    number,
    onClose,
    onPullsRefresh,
  }: Props = $props();

  function handleKeydown(e: KeyboardEvent): void {
    // A dialog above the drawer (e.g. the merge modal) owns Escape via the
    // modal stack; its kit-ui handler runs after this one, so the stack —
    // not defaultPrevented — is the signal to stand down.
    if (getStackDepth() > 0) return;
    if (e.key === "Escape" && !e.defaultPrevented) {
      e.preventDefault();
      onClose();
    }
  }

  $effect(() => {
    window.addEventListener("keydown", handleKeydown);
    return () => window.removeEventListener("keydown", handleKeydown);
  });
</script>

<div class="drawer-backdrop">
  <aside class="drawer-panel">
    <div class="drawer-header">
      <button class="close-btn" onclick={onClose} title="Close (Esc)">&#x2715;</button>
      <span class="drawer-title">
        {owner}/{name}#{number}
      </span>
    </div>
    <div class="drawer-body">
      {#key `${provider}/${platformHost}/${owner}/${name}/${number}`}
        {#if itemType === "pr"}
          <PullDetail
            {provider}
            {platformHost}
            {owner}
            {name}
            {repoPath}
            {number}
            {...(onPullsRefresh ? { onPullsRefresh } : {})}
          />
        {:else}
          <IssueDetail {provider} {platformHost} {owner} {name} {repoPath} {number} />
        {/if}
      {/key}
    </div>
  </aside>
</div>

<style>
  .drawer-backdrop {
    position: fixed;
    top: var(--header-height);
    left: 0;
    right: 0;
    bottom: var(--status-bar-height);
    z-index: 100;
  }

  .drawer-panel {
    position: absolute;
    top: 0;
    right: 0;
    bottom: 0;
    width: 100%;
    background: var(--bg-surface);
    border-left: 1px solid var(--border-default);
    box-shadow: var(--shadow-lg);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .drawer-header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-default);
    flex-shrink: 0;
  }

  .close-btn {
    padding: 4px 8px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    font-size: var(--font-size-lg);
  }

  .close-btn:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .drawer-title {
    font-size: var(--font-size-sm);
    color: var(--text-muted);
  }

  .drawer-body {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
</style>
