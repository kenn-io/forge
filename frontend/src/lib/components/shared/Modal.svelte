<script lang="ts">
  import { Modal as KitModal } from "@kenn-io/kit-ui";
  import { untrack } from "svelte";
  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import type { Snippet } from "svelte";

  // Shared in-app dialog shell: adapts kit-ui Modal (backdrop, frame, header,
  // focus trap, scroll lock, Escape/overlay close, focus restore) to this
  // app's contract — an `open` prop, pixel widths, and a keyboard modal-stack
  // frame so global shortcuts stay suppressed while any dialog is open.
  // Feature components own only their body content and domain actions.
  interface Props {
    open: boolean;
    title?: string | undefined;
    ariaLabel?: string | undefined;
    width?: number | undefined;
    frameId?: string | undefined;
    // Header close (X) button, off by default. Dialogs here provide an explicit
    // Cancel/close in their footer, so an X would duplicate it and add a stop to
    // the focus trap. Opt in with showClose only for content-only dialogs that
    // have no other dismiss control (Escape and backdrop click always close).
    showClose?: boolean;
    onClose: () => void;
    children: Snippet;
    footer?: Snippet | undefined;
  }

  let {
    open,
    title = undefined,
    ariaLabel = undefined,
    width = 440,
    frameId = undefined,
    showClose = false,
    onClose,
    children,
    footer = undefined,
  }: Props = $props();

  let bodyEl: HTMLElement | null = $state(null);
  let footEl: HTMLElement | null = $state(null);

  // Every dialog registers on the modal stack, not just those with an
  // explicit frameId: the keyboard dispatcher suppresses global single-key
  // shortcuts while the stack is non-empty, and leaves Escape for the kit
  // modal to consume — so Escape peels the dialog instead of triggering a
  // background action underneath it.
  $effect(() => {
    if (!open) return;
    return untrack(() => pushModalFrame(frameId ?? "shared-modal", []));
  });

  $effect(() => {
    if (!open) return;
    queueMicrotask(() => {
      // Prefer a body input, then a body or footer control, so confirmation
      // dialogs land on their first action (Cancel) instead of the panel
      // itself; kit's focus trap already handles the no-focusable fallback.
      const primary = bodyEl?.querySelector<HTMLElement>(
        "input:not([type='hidden']):not([disabled]), textarea:not([disabled])",
      );
      const action =
        bodyEl?.querySelector<HTMLElement>("button:not([disabled])") ??
        footEl?.querySelector<HTMLElement>("button:not([disabled])");
      (primary ?? action)?.focus();
    });
  });
</script>

{#snippet footerContent()}
  <div class="modal-scope" bind:this={footEl}>
    {@render footer?.()}
  </div>
{/snippet}

{#if open}
  <!-- Conditional spreads: kit Modal's optional props are declared without
       `| undefined`, so explicit undefined fails under
       exactOptionalPropertyTypes. Omit the props instead. -->
  <KitModal
    {...title !== undefined ? { title } : {}}
    {...ariaLabel !== undefined ? { ariaLabel } : {}}
    {...footer ? { footer: footerContent } : {}}
    width="100%"
    maxWidth={`min(${width}px, calc(100vw - 40px))`}
    closable={showClose}
    onclose={onClose}
  >
    <div class="modal-scope" bind:this={bodyEl}>
      {@render children()}
    </div>
  </KitModal>
{/if}

<style>
  /* Layout-transparent wrappers that exist only to scope the initial-focus
     query per dialog instance (stacked dialogs must not steal each other's
     focus target). */
  .modal-scope {
    display: contents;
  }
</style>
