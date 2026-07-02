<script lang="ts">
  import { Button } from "@middleman/ui";

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

<div class={["repo-state", `repo-state--${tone}`]}>
  <h2>{title}</h2>
  <p>{message}</p>
  {#if actionLabel && onaction}
    <Button tone="info" surface="soft" onclick={onaction}>
      {actionLabel}
    </Button>
  {/if}
</div>

<style>
  .repo-state {
    max-width: 520px;
    padding: 20px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-surface);
    box-shadow: var(--shadow-sm);
  }

  .repo-state--error {
    border-color: color-mix(
      in srgb,
      var(--accent-red) 38%,
      var(--border-default)
    );
  }

  .repo-state h2 {
    margin-bottom: 6px;
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    font-weight: 600;
  }

  .repo-state p {
    margin-bottom: 12px;
    color: var(--text-secondary);
  }
</style>
