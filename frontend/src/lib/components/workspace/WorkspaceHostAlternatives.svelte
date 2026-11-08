<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import type { WorkspaceHostChoice } from "../../stores/workspace-target.svelte.js";

  interface Props {
    /** Why the default machine cannot take the workspace. */
    reason: string;
    /** Machines that can take it now. */
    choices: readonly WorkspaceHostChoice[];
    disabled?: boolean;
    onChoose: (choice: WorkspaceHostChoice) => void;
  }

  let { reason, choices, disabled = false, onChoose }: Props = $props();
</script>

<div class="workspace-host-alternatives" role="group" aria-label="Create on another machine">
  <p class="workspace-host-alternatives__reason" role="status">{reason}</p>
  <div class="workspace-host-alternatives__choices">
    {#each choices as choice (choice.hostKey ?? "")}
      <Button
        size="sm"
        tone="neutral"
        surface="soft"
        {disabled}
        label={`Create on ${choice.label}`}
        onclick={() => onChoose(choice)}
      />
    {/each}
  </div>
</div>

<style>
  .workspace-host-alternatives {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    flex-basis: 100%;
  }

  .workspace-host-alternatives__reason {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .workspace-host-alternatives__choices {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
</style>
