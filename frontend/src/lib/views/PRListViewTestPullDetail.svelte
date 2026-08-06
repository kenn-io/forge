<script lang="ts">
  import type { InlineWorkspaceController } from "../workspace-inline.js";

  interface Props {
    owner: string;
    name: string;
    number: number;
    inlineWorkspace?: InlineWorkspaceController | null;
  }

  const { owner, name, number, inlineWorkspace = null }: Props = $props();
</script>

<div data-testid="pull-detail" data-has-inline-workspace={inlineWorkspace !== null}>
  Conversation {owner}/{name}#{number}
</div>
