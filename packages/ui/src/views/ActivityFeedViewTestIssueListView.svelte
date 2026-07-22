<script lang="ts">
  import type { InlineWorkspaceController } from "../workspace-inline.js";

  interface Props {
    selectedIssue: { owner: string; name: string; number: number };
    renderWorkspaceDock?: boolean;
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  const { selectedIssue, renderWorkspaceDock = true, inlineWorkspace = null }: Props = $props();
</script>

<div
  data-testid="embedded-issue-list-view"
  data-render-workspace-dock={renderWorkspaceDock}
  data-has-inline-workspace={inlineWorkspace !== null}
>
  Issue {selectedIssue.owner}/{selectedIssue.name}#{selectedIssue.number}
</div>
