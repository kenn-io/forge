<script lang="ts">
  import type { KeySpec } from "../../stores/keyboard/keyspec.js";
  import { kbdAriaLabel, kbdGlyph, kbdGlyphJoiner } from "./useKbdLabel.js";

  interface Props {
    binding: KeySpec;
  }

  let { binding }: Props = $props();

  const glyph = $derived(kbdGlyph(binding));
  const joiner = $derived(kbdGlyphJoiner());
  const aria = $derived(kbdAriaLabel(binding));
</script>

<kbd class="kbd-badge" data-joiner={joiner === "" ? "compact" : "plus"} aria-label={aria}>
  {glyph}
  <span class="sr-only">{aria}</span>
</kbd>

<style>
  .kbd-badge {
    display: inline-flex;
    align-items: center;
    padding: 1px 5px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    font-size: var(--font-size-xs);
    line-height: 1;
    color: var(--text-secondary);
    background: var(--bg-inset);
    font-family: ui-monospace, monospace;
  }
  .kbd-badge[data-joiner="compact"] {
    font-family: var(--font-sans);
    letter-spacing: 0.07em;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
  }
  @media (pointer: coarse) {
    .kbd-badge {
      display: none;
    }
  }
</style>
