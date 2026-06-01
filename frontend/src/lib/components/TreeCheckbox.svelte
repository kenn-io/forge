<script lang="ts">
  import type { SelectionState } from "./repoTree.js";

  interface Props {
    /** Tri-state value. Drives the visual and the native input's properties. */
    value: SelectionState;
    /**
     * Decorative checkboxes (e.g. the "All repos" row) are not interactive:
     * the input is hidden from assistive tech and the control ignores pointer
     * events so clicks fall through to the owning row.
     */
    decorative?: boolean;
    /** Fires on the input's mousedown; selection happens here, never on click. */
    onmousedown?: (event: MouseEvent) => void;
  }

  let { value, decorative = false, onmousedown }: Props = $props();

  let inputEl = $state<HTMLInputElement>();

  // `indeterminate` is a DOM property, not an attribute, so it must be set
  // imperatively. Keeping it on the real input preserves the partial state for
  // assistive tech and for tests that read `.indeterminate`.
  $effect(() => {
    if (inputEl) inputEl.indeterminate = value === "partial";
  });

  // A native checkbox toggles its own `checked` as the click default action,
  // which would fight the controlled `checked={state === "checked"}` binding and
  // leave the box showing the inverse of the real selection. Cancel that default
  // action so the input is purely controlled by `state`.
  function suppressNativeToggle(event: MouseEvent): void {
    event.preventDefault();
  }
</script>

<span
  class="tree-check"
  class:tree-check--checked={value === "checked"}
  class:tree-check--partial={value === "partial"}
  class:tree-check--decorative={decorative}
>
  <input
    bind:this={inputEl}
    class="tree-check__input"
    type="checkbox"
    checked={value === "checked"}
    tabindex="-1"
    aria-hidden={decorative ? "true" : undefined}
    onmousedown={onmousedown}
    onclick={suppressNativeToggle}
  />
  <span class="tree-check__box" aria-hidden="true">
    {#if value === "checked"}
      <svg
        class="tree-check__glyph"
        viewBox="0 0 16 16"
        fill="none"
        aria-hidden="true"
      >
        <path
          d="M4 8.5l2.6 2.6L12 5.4"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
    {:else if value === "partial"}
      <svg
        class="tree-check__glyph"
        viewBox="0 0 16 16"
        fill="none"
        aria-hidden="true"
      >
        <path d="M4.5 8h7" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
      </svg>
    {/if}
  </span>
</span>

<style>
  .tree-check {
    position: relative;
    width: 16px;
    height: 16px;
    flex-shrink: 0;
    display: inline-flex;
  }

  .tree-check--decorative {
    pointer-events: none;
  }

  /* The real input sits transparently on top of the drawn box and is the click
     target; the box behind it carries all the visuals. */
  .tree-check__input {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    opacity: 0;
    cursor: pointer;
  }

  .tree-check__box {
    pointer-events: none;
    box-sizing: border-box;
    width: 16px;
    height: 16px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1.5px solid var(--border-default);
    border-radius: 5px;
    background: var(--bg-surface);
    /* Glyph color: surface-colored so it contrasts against the accent fill in
       both themes (white check on dark-blue light theme; dark check on
       light-blue dark theme). */
    color: var(--bg-surface);
    transition:
      background-color 0.15s ease,
      border-color 0.15s ease;
  }

  .tree-check:hover .tree-check__box {
    border-color: var(--accent-blue);
  }

  .tree-check--checked .tree-check__box,
  .tree-check--partial .tree-check__box {
    background: var(--accent-blue);
    border-color: var(--accent-blue);
  }

  .tree-check__glyph {
    width: 12px;
    height: 12px;
    animation: tree-check-pop 0.14s ease-out;
  }

  @keyframes tree-check-pop {
    from {
      transform: scale(0.5);
      opacity: 0;
    }
    to {
      transform: scale(1);
      opacity: 1;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .tree-check__glyph {
      animation: none;
    }
  }
</style>
