<script lang="ts">
  import { Checkbox } from "@kenn-io/kit-ui";
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

</script>

<span
  class="tree-check"
  class:tree-check--decorative={decorative}
  {@attach (element) => {
    const input = element.querySelector<HTMLInputElement>("input[type='checkbox']");
    if (!input) return;
    input.tabIndex = -1;
    if (decorative) input.setAttribute("aria-hidden", "true");
    else input.removeAttribute("aria-hidden");
    element.onmousedown = decorative ? null : onmousedown ?? null;
    element.onclick = (event) => event.preventDefault();
    return () => {
      element.onmousedown = null;
      element.onclick = null;
    };
  }}
>
  <Checkbox
    class="tree-check__control"
    checked={value === "checked"}
    indeterminate={value === "partial"}
  />
</span>

<style>
  .tree-check {
    display: inline-flex;
    width: 16px;
    height: 16px;
    flex-shrink: 0;
  }

  .tree-check--decorative {
    pointer-events: none;
  }

  .tree-check :global(.tree-check__control) {
    width: 16px;
    height: 16px;
    gap: 0;
  }
</style>
