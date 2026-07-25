<script lang="ts">
  import type { InlineWorkspaceController } from "../workspace-inline.js";

  interface Props {
    selectedIssue: { owner: string; name: string; number: number };
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  const { selectedIssue, inlineWorkspace = null }: Props = $props();
</script>

<div
  data-testid="embedded-issue-list-view"
  data-has-inline-workspace={inlineWorkspace !== null}
>
  Issue {selectedIssue.owner}/{selectedIssue.name}#{selectedIssue.number}
</div>
