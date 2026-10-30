<script lang="ts">
  import type { Snippet } from "svelte";
  import Modal from "../shared/Modal.svelte";

  // Phone presentation of a label or user picker. Desktop anchors these
  // pickers under their trigger; a phone shows them as a modal instead.
  interface Props {
    ariaLabel: string;
    /** The picker's own frame, for the host's outside-press dismissal. */
    element?: HTMLDivElement | undefined;
    onclose: () => void;
    children: Snippet;
  }

  let { ariaLabel, element = $bindable(), onclose, children }: Props = $props();
</script>

<Modal open {ariaLabel} width={360} onClose={onclose}>
  <!-- data-autofocus parks initial focus on the frame, so opening the
       picker does not raise the software keyboard. -->
  <div class="picker-modal" bind:this={element} tabindex="-1" data-autofocus>
    {@render children()}
  </div>
</Modal>

<style>
  /* The picker draws its own header and close button, so it fills the
     panel: the negative margin cancels kit Modal's 16px body padding, which
     also keeps presses inside the panel within the host's picker frame. */
  .picker-modal {
    margin: -16px;
    outline: none;
    --label-picker-max-height: calc(100svh - 80px);
    --user-picker-max-height: calc(100svh - 80px);
  }

  .picker-modal > :global(.kit-popover-card) {
    border: 0;
    box-shadow: none;
  }
</style>
