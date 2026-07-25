<script lang="ts">
  import type { InlineWorkspaceController } from "../workspace-inline.js";

  interface Props {
    selectedPR: { owner: string; name: string; number: number };
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  const { selectedPR, inlineWorkspace = null }: Props = $props();
</script>

<div
  data-testid="embedded-pr-list-view"
  data-has-inline-workspace={inlineWorkspace !== null}
>
  PR {selectedPR.owner}/{selectedPR.name}#{selectedPR.number}
</div>
