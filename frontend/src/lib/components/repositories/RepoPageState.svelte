<script lang="ts">
  import { Button } from "@middleman/ui";
  import { Card } from "@kenn-io/kit-ui";

  interface Props {
    title: string;
    message: string;
    tone?: "neutral" | "error";
    actionLabel?: string;
    onaction?: () => void;
  }

  let {
    title,
    message,
    tone = "neutral",
    actionLabel = undefined,
    onaction = undefined,
  }: Props = $props();
</script>

<Card level="raised" padding="md" class={["repo-state", `repo-state--${tone}`].join(" ")}>
  <h2>{title}</h2>
  <p>{message}</p>
  {#if actionLabel && onaction}
    <Button tone="info" surface="soft" onclick={onaction}>
      {actionLabel}
    </Button>
  {/if}
</Card>

<style>
  :global(.repo-state) {
    max-width: 520px;
  }

  :global(.repo-state--error) {
    border-color: color-mix(
      in srgb,
      var(--accent-red) 38%,
      var(--border-default)
    );
  }

  :global(.repo-state) h2 {
    margin-bottom: 6px;
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    font-weight: 600;
  }

  :global(.repo-state) p {
    margin-bottom: 12px;
    color: var(--text-secondary);
  }
</style>
