<script lang="ts">
  import type { Snippet } from "svelte";

  type Tone = "neutral" | "success" | "danger" | "info" | "workflow";
  type Surface = "outline" | "soft" | "solid";
  type Size = "sm" | "md";

  interface Props {
    tone?: Tone;
    surface?: Surface;
    size?: Size;
    type?: "button" | "submit" | "reset";
    disabled?: boolean;
    title?: string;
    ariaLabel?: string | undefined;
    label?: string;
    shortLabel?: string;
    ariaExpanded?: boolean;
    class?: string;
    onclick?: ((event: MouseEvent) => void) | undefined;
    children?: Snippet;
    trailing?: Snippet;
  }

  let {
    tone = "neutral",
    surface = "outline",
    size = "md",
    type = "button",
    disabled = false,
    title = undefined,
    ariaLabel = undefined,
    label = undefined,
    shortLabel = undefined,
    ariaExpanded = undefined,
    class: className = "",
    onclick = undefined,
    children,
    trailing,
  }: Props = $props();

  const classes = $derived(
    [
      "action-button",
      `action-button--${tone}`,
      `action-button--${surface}`,
      `action-button--${size}`,
      className,
    ].filter(Boolean).join(" "),
  );
</script>

<button
  {type}
  class={classes}
  {disabled}
  {title}
  aria-expanded={ariaExpanded}
  aria-label={ariaLabel ?? (label && shortLabel ? label : undefined)}
  onclick={onclick}
>
  {#if children}
    {@render children()}
  {/if}
  {#if label}
    <span class="action-button__label">{label}</span>
  {/if}
  {#if shortLabel}
    <span class="action-button__short-label">{shortLabel}</span>
  {/if}
  {#if trailing}
    {@render trailing()}
  {/if}
</button>

<style>
  .action-button {
    box-sizing: border-box;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    min-height: 28px;
    font-size: var(--font-size-md);
    font-weight: 500;
    padding: 6px 14px;
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition:
      background-color 0.12s ease,
      border-color 0.12s ease,
      color 0.12s ease,
      box-shadow 0.12s ease,
      transform 0.08s ease,
      opacity 0.1s ease;
    white-space: nowrap;
    line-height: 1;
  }

  .action-button:focus-visible {
    outline: none;
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-blue) 42%, transparent);
  }

  .action-button:active:not(:disabled) {
    transform: translateY(1px);
  }

  .action-button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .action-button--sm {
    min-height: 24px;
    padding: 4px 12px;
    border-radius: 6px;
    font-size: var(--font-size-sm);
  }

  .action-button :global(svg) {
    flex-shrink: 0;
  }

  .action-button__short-label {
    display: none;
  }

  /* Neutral outline — cancel / secondary */
  .action-button--outline.action-button--neutral {
    background: var(--bg-inset);
    color: var(--text-secondary);
    border: 1px solid var(--border-default);
  }
  .action-button--outline.action-button--neutral:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  /* Neutral soft — low-emphasis secondary actions */
  .action-button--soft.action-button--neutral {
    background: color-mix(in srgb, var(--text-muted) 8%, var(--bg-inset));
    color: var(--text-secondary);
    border: 1px solid color-mix(in srgb, var(--border-default) 80%, transparent);
  }
  .action-button--soft.action-button--neutral:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: var(--border-default);
  }

  /* Danger outline — close (neutral at rest, red on hover) */
  .action-button--outline.action-button--danger {
    background: var(--bg-surface);
    color: var(--text-secondary);
    border: 1px solid var(--border-default);
  }
  .action-button--outline.action-button--danger:hover:not(:disabled) {
    background: var(--accent-red, #d73a49);
    color: #fff;
    border-color: var(--accent-red, #d73a49);
  }

  /* Success solid — merge, reopen, confirm */
  .action-button--solid.action-button--success {
    background: #1a7f37;
    color: #e6ffe6;
    border: 1px solid #1a7f37;
  }
  .action-button--solid.action-button--success:hover:not(:disabled) {
    background: #176b2e;
    border-color: #176b2e;
  }
  .action-button--solid.action-button--success:focus-visible {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-green) 48%, transparent);
  }
  .action-button--solid.action-button--success:active:not(:disabled),
  .action-button--solid.action-button--success[aria-expanded="true"] {
    background: #145c27;
    border-color: #145c27;
  }

  /* Success soft — approve */
  .action-button--soft.action-button--success {
    background: color-mix(in srgb, var(--accent-green) 12%, transparent);
    color: var(--accent-green);
    border: 1px solid color-mix(in srgb, var(--accent-green) 30%, transparent);
  }
  .action-button--soft.action-button--success:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-green) 20%, transparent);
  }
  .action-button--soft.action-button--success:focus-visible {
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent-green) 38%, transparent);
  }
  .action-button--soft.action-button--success:active:not(:disabled),
  .action-button--soft.action-button--success[aria-expanded="true"] {
    background: color-mix(in srgb, var(--accent-green) 26%, transparent);
    border-color: color-mix(in srgb, var(--accent-green) 48%, transparent);
  }

  /* Info soft — ready for review */
  .action-button--soft.action-button--info {
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    color: var(--accent-blue);
    border: 1px solid color-mix(in srgb, var(--accent-blue) 30%, transparent);
  }
  .action-button--soft.action-button--info:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-blue) 18%, transparent);
  }

  /* Workflow soft — approve workflows */
  .action-button--soft.action-button--workflow {
    background: color-mix(in srgb, var(--accent-purple) 12%, transparent);
    color: var(--accent-purple);
    border: 1px solid color-mix(in srgb, var(--accent-purple) 30%, transparent);
  }
  .action-button--soft.action-button--workflow:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-purple) 20%, transparent);
  }
</style>
