<script module lang="ts">
  /** A press inside a phone modal is never an outside press for the page
   * popover that opened it; the modal owns its own dismissal. */
  export function pressedInPhoneModal(target: EventTarget | null): boolean {
    return target instanceof Element && target.closest("[data-phone-modal]") !== null;
  }
</script>

<script lang="ts">
  import type { ComponentProps } from "svelte";
  import { getRoute } from "../../stores/router.svelte.js";
  import Modal from "./Modal.svelte";

  // A modal for the phone shell. It mounts on document.body so no stacking
  // context around its trigger can paint over the backdrop. It closes when
  // the route changes: Android Back while it is open switches the detail tab,
  // and a modal left open would cover the next view or trap it invisibly.
  const props: Omit<ComponentProps<typeof Modal>, "open"> = $props();

  const openedOn = getRoute();
  $effect(() => {
    if (getRoute() !== openedOn) props.onClose();
  });

  function portalToBody(node: HTMLElement): () => void {
    document.body.appendChild(node);
    return () => node.remove();
  }
</script>

<div class="phone-modal" data-phone-modal {@attach portalToBody}>
  <Modal open {...props} />
</div>

<style>
  .phone-modal {
    display: contents;
  }
</style>
