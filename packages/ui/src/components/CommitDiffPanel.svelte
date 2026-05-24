<script lang="ts">
  import { onMount } from "svelte";
  import { getStores } from "../context.js";
  import DiffSidebar from "./diff/DiffSidebar.svelte";
  import DiffToolbar from "./diff/DiffToolbar.svelte";
  import DiffView from "./diff/DiffView.svelte";

  const { diff } = getStores();

  interface Props {
    provider: string;
    platformHost?: string | undefined;
    owner: string;
    name: string;
    repoPath: string;
    commitSha: string;
  }

  const {
    provider,
    platformHost,
    owner,
    name,
    repoPath,
    commitSha,
  }: Props = $props();

  onMount(() => {
    void diff.loadCommitDiff(
      { provider, platformHost, owner, name, repoPath },
      commitSha,
    );

    return () => diff.clearDiff();
  });
</script>

<div class="commit-diff-panel">
  <DiffToolbar showScopePicker={false} showRichPreview={false} showFileJump={true} />
  <div class="files-layout">
    <aside class="files-sidebar" aria-label="Changed files">
      <DiffSidebar showCommits={false} resetKey={commitSha} />
    </aside>
    <div class="files-main">
      <DiffView
        {provider}
        {platformHost}
        {owner}
        {name}
        {repoPath}
        number={0}
        loadOnMount={false}
        richPreviewEnabled={false}
      />
    </div>
  </div>
</div>

<style>
  .commit-diff-panel {
    min-height: 0;
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .files-layout {
    min-height: 0;
    flex: 1;
    display: flex;
    overflow: hidden;
  }

  .files-sidebar {
    width: 260px;
    flex-shrink: 0;
    border-right: 1px solid var(--border-default);
    background: var(--bg-surface);
    overflow-y: auto;
    display: flex;
    flex-direction: column;
  }

  .files-main {
    min-width: 0;
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  @media (max-width: 720px) {
    .files-layout {
      flex-direction: column;
    }

    .files-sidebar {
      width: 100%;
      max-height: 35vh;
      border-right: none;
      border-bottom: 1px solid var(--border-default);
    }
  }
</style>
