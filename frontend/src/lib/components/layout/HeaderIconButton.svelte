<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    title: string;
    disabled?: boolean;
    active?: boolean;
    onclick?: (event: MouseEvent) => void;
    children?: Snippet;
  }

  let {
    title,
    disabled = false,
    active = false,
    onclick = undefined,
    children,
  }: Props = $props();
</script>

<button
  type="button"
  {title}
  aria-label={title}
  {disabled}
  {onclick}
  data-active={active ? "true" : undefined}
>
  {#if children}
    {@render children()}
  {/if}
</button>

<style>
  button {
    box-sizing: border-box;
    min-width: 34px;
    min-height: 28px;
    padding: 5px 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 7px;
    line-height: 0;
    transition: background 0.15s, color 0.15s, border-color 0.15s;
  }

  button:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: var(--border-muted);
  }

  button:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  button[data-active="true"] {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: var(--border-muted);
  }

  button :global(svg) {
    flex-shrink: 0;
  }
</style>
