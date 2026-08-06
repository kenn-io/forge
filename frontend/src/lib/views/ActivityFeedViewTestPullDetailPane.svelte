<script lang="ts">
  import type { InlineWorkspaceController } from "../workspace-inline.js";

  interface Props {
    tabKey: string;
    visible: boolean;
    pr: { owner: string; name: string; number: number };
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  const { tabKey, visible, pr, inlineWorkspace = null }: Props = $props();
</script>

<div
  data-testid="pull-detail-pane"
  data-tab-key={tabKey}
  data-visible={String(visible)}
  data-has-inline-workspace={inlineWorkspace !== null}
>
  PR {pr.owner}/{pr.name}#{pr.number} {tabKey}
</div>
