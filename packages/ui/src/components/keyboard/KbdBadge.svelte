<script lang="ts">
  import { KbdBadge } from "@kenn-io/kit-ui";
  import type { KeySpec } from "../../stores/keyboard/keyspec.js";
  import { kbdAriaLabel, kbdGlyphJoiner, kbdGlyphParts } from "./useKbdLabel.js";

  interface Props {
    binding: KeySpec;
  }

  let { binding }: Props = $props();
</script>

<KbdBadge
  keys={kbdGlyphParts(binding)}
  joiner={kbdGlyphJoiner() === "" ? "compact" : "plus"}
  ariaLabel={kbdAriaLabel(binding)}
/>
