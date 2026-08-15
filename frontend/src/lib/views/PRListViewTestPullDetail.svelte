<script lang="ts">
  import type { InlineWorkspaceController } from "../workspace-inline.js";

  interface Props {
    owner: string;
    name: string;
    number: number;
    inlineWorkspace?: InlineWorkspaceController | null;
    onDetailTabChange?: ((tab: "conversation" | "files") => void) | undefined;
  }

  const {
    owner,
    name,
    number,
    inlineWorkspace = null,
    onDetailTabChange,
  }: Props = $props();
</script>

<div data-testid="pull-detail" data-has-inline-workspace={inlineWorkspace !== null}>
  Conversation {owner}/{name}#{number}
  <button type="button" onclick={() => onDetailTabChange?.("files")}>Jump to diff</button>
</div>
