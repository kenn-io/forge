<script lang="ts">
  import { onMount } from "svelte";
  import { getStores } from "../context.js";
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

<style>
  .commit-diff-panel {
    min-height: 0;
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
</style>
