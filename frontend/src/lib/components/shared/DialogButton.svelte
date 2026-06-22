<script lang="ts">
  import type { Snippet } from "svelte";

  // Shared modal footer button. Keep modal action sizing and tones here so
  // custom modal bodies can still avoid local footer-button CSS.
  interface Props {
    type?: "button" | "submit" | "reset";
    tone?: "secondary" | "primary" | "danger";
    form?: string | undefined;
    disabled?: boolean;
    onclick?: ((event: MouseEvent) => void) | undefined;
    children: Snippet;
  }

  let {
    type = "button",
    tone = "secondary",
    form = undefined,
    disabled = false,
    onclick = undefined,
    children,
  }: Props = $props();
</script>

<button
  {type}
  {form}
  class:dialog-button-secondary={tone === "secondary"}
  class:dialog-button-primary={tone === "primary"}
  class:dialog-button-danger={tone === "danger"}
  class="dialog-button"
  {disabled}
  {onclick}
>
  {@render children()}
</button>

<style>
  .dialog-button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 30px;
    padding: 0 14px;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
    font-weight: 600;
    cursor: pointer;
    transition: background-color 80ms ease, color 80ms ease,
      border-color 80ms ease, filter 80ms ease;
  }

  .dialog-button-secondary {
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-secondary);
  }

  .dialog-button-secondary:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .dialog-button-primary {
    border: 1px solid var(--accent-blue);
    background: var(--accent-blue);
    color: #fff;
  }

  .dialog-button-danger {
    border: 1px solid var(--accent-red);
    background: var(--accent-red);
    color: #fff;
  }

  .dialog-button-primary:hover:not(:disabled),
  .dialog-button-danger:hover:not(:disabled) {
    filter: brightness(0.95);
  }

  .dialog-button:disabled {
    cursor: not-allowed;
    opacity: 0.6;
  }
</style>
