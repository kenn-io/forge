<script lang="ts">
  import MinusIcon from "@lucide/svelte/icons/minus";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import {
    MAX_TERMINAL_FONT_SIZE,
    MIN_TERMINAL_FONT_SIZE,
  } from "./terminalZoom";

  interface Props {
    fontSize: number;
    disabled?: boolean;
    onDecrease: () => void;
    onIncrease: () => void;
    onReset: () => void;
  }

  const {
    fontSize,
    disabled = false,
    onDecrease,
    onIncrease,
    onReset,
  }: Props = $props();
</script>

<div class="terminal-zoom" role="group" aria-label="Terminal font size">
  <button
    type="button"
    aria-label="Decrease terminal font size"
    title="Decrease terminal font size"
    disabled={disabled || fontSize <= MIN_TERMINAL_FONT_SIZE}
    onclick={onDecrease}
  >
    <MinusIcon size="12" strokeWidth="2.2" aria-hidden="true" />
  </button>
  <button
    class="font-size"
    type="button"
    aria-label="Reset terminal font size"
    title="Reset terminal font size"
    {disabled}
    onclick={onReset}
  >
    {fontSize}px
  </button>
  <button
    type="button"
    aria-label="Increase terminal font size"
    title="Increase terminal font size"
    disabled={disabled || fontSize >= MAX_TERMINAL_FONT_SIZE}
    onclick={onIncrease}
  >
    <PlusIcon size="12" strokeWidth="2.2" aria-hidden="true" />
  </button>
</div>

<style>
  .terminal-zoom {
    display: inline-flex;
    height: 22px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    overflow: hidden;
    background: var(--bg-surface);
  }

  button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 23px;
    padding: 0 5px;
    border: 0;
    border-right: 1px solid var(--border-default);
    background: transparent;
    color: var(--text-secondary);
    font: inherit;
    cursor: pointer;
  }

  button:last-child {
    border-right: 0;
  }

  button:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  button:focus-visible {
    position: relative;
    z-index: 1;
    outline: 1px solid var(--accent-blue);
    outline-offset: -1px;
  }

  button:disabled {
    color: var(--text-muted);
    cursor: not-allowed;
    opacity: 0.55;
  }

  .font-size {
    min-width: 42px;
    color: var(--text-primary);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    font-variant-numeric: tabular-nums;
  }
</style>
