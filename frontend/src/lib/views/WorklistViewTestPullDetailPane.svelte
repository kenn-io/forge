<script lang="ts">
  import { buildPullRequestFilesRoute } from "../routes.js";
  import { navigate } from "../stores/router.svelte.js";
  import type { InlineWorkspaceController } from "../workspace-inline.js";

  interface Props {
    tabKey: string;
    visible: boolean;
    keyboardActive?: boolean;
    pr: { owner: string; name: string; number: number; provider: string; platformHost?: string; repoPath: string };
    onDetailTabChange?: ((tab: "conversation" | "files") => void) | undefined;
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  const { tabKey, visible, keyboardActive = true, pr, onDetailTabChange, inlineWorkspace = null }: Props = $props();

  // A review thread's "Jump to diff" asks the host to show the files pane and
  // otherwise opens the standalone files route, as the real detail does.
  function jumpToDiff(): void {
    if (onDetailTabChange) onDetailTabChange("files");
    else navigate(buildPullRequestFilesRoute(pr));
  }
</script>

<div
  data-testid="pull-detail-pane"
  data-tab-key={tabKey}
  data-visible={String(visible)}
  data-keyboard-active={String(keyboardActive)}
  data-has-inline-workspace={inlineWorkspace !== null}
>
  PR {pr.owner}/{pr.name}#{pr.number} {tabKey}
  {#if tabKey === "conversation"}
    <button type="button" onclick={jumpToDiff}>Jump to diff</button>
  {/if}
</div>
