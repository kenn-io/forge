<script lang="ts">
  import type { InlineWorkspaceController } from "../workspace-inline.js";

  interface Props {
    selectedPR: { owner: string; name: string; number: number };
    renderWorkspaceDock?: boolean;
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  const { selectedPR, renderWorkspaceDock = true, inlineWorkspace = null }: Props = $props();
</script>

<div
  data-testid="embedded-pr-list-view"
  data-render-workspace-dock={renderWorkspaceDock}
  data-has-inline-workspace={inlineWorkspace !== null}
>
  PR {selectedPR.owner}/{selectedPR.name}#{selectedPR.number}
</div>
