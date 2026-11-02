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

  // A modal for the phone shell. It mounts at the top of the phone layout, so
  // no stacking context around its trigger can paint over the backdrop, while
  // it keeps the phone field and touch-target sizing that layout carries. It
  // closes when the route changes: Android Back while it is open switches the
  // detail tab, and a modal left open would cover the next view or trap it
  // invisibly.
  type Props = Omit<ComponentProps<typeof Modal>, "open"> & {
    /** Closes on navigation; defaults to onClose. Hosts whose onClose
     * refuses while busy pass one that always closes. */
    onRouteChange?: (() => void) | undefined;
  };
  const { onRouteChange, ...props }: Props = $props();

  const openedOn = getRoute();
  $effect(() => {
    if (getRoute() !== openedOn) (onRouteChange ?? props.onClose)();
  });

  function mountInPhoneLayout(node: HTMLElement): () => void {
    const host = node.parentElement?.closest(".focus-layout--phone")
      ?? document.getElementById("app")
      ?? document.body;
    host.appendChild(node);
    return () => node.remove();
  }
</script>

<div class="phone-modal" data-phone-modal {@attach mountInPhoneLayout}>
  <Modal open {...props} />
</div>

<style>
  .phone-modal {
    display: contents;
  }
</style>
