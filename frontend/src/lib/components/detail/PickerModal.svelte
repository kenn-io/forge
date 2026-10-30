<script lang="ts">
  import type { Snippet } from "svelte";
  import PhoneModal from "../shared/PhoneModal.svelte";

  // Phone presentation of a label or user picker. Desktop anchors these
  // pickers under their trigger; a phone shows them as a modal instead.
  interface Props {
    ariaLabel: string;
    onclose: () => void;
    children: Snippet;
  }

  const { ariaLabel, onclose, children }: Props = $props();
</script>

<PhoneModal {ariaLabel} width={360} onClose={onclose}>
  <!-- data-autofocus parks initial focus on the frame, so opening the
       picker does not raise the software keyboard. -->
  <div class="picker-modal" tabindex="-1" data-autofocus>
    {@render children()}
  </div>
</PhoneModal>

<style>
  /* The picker draws its own header and close button, so it fills the
     panel: the negative margin cancels kit Modal's 16px body padding. */
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
