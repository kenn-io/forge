<script lang="ts">
  import type { Snippet } from "svelte";
  import DialogButton from "./DialogButton.svelte";
  import Modal from "./Modal.svelte";

  // Shared confirmation dialog for simple cancel/confirm flows. Use this
  // instead of rebuilding modal body copy, footer buttons, or destructive
  // button treatment in feature components.
  interface Props {
    open: boolean;
    title: string;
    message: string;
    hint?: string | undefined;
    confirmLabel: string;
    pendingLabel?: string | undefined;
    busy?: boolean;
    /** Holds the confirm button until the choices in `children` are complete. */
    confirmDisabled?: boolean;
    tone?: "primary" | "danger";
    width?: number;
    frameId?: string | undefined;
    onCancel: () => void;
    onConfirm: () => void;
    /** Choices the confirmation needs, shown between the message and hint. */
    children?: Snippet | undefined;
  }

  let {
    open,
    title,
    message,
    hint = undefined,
    confirmLabel,
    pendingLabel = undefined,
    busy = false,
    confirmDisabled = false,
    tone = "primary",
    width = 420,
    frameId = undefined,
    onCancel,
    onConfirm,
    children = undefined,
  }: Props = $props();

  function closeDialog(): void {
    if (busy) return;
    onCancel();
  }
</script>

<Modal {open} {title} onClose={closeDialog} {width} {frameId}>
  <div class="confirm-content">
    <p class="confirm-message">{message}</p>
    {@render children?.()}
    {#if hint}
      <p class="confirm-hint">{hint}</p>
    {/if}
  </div>

  {#snippet footer()}
    <DialogButton disabled={busy} onclick={onCancel}>
      Cancel
    </DialogButton>
    <DialogButton
      tone={tone}
      disabled={busy || confirmDisabled}
      onclick={onConfirm}
    >
      {busy && pendingLabel ? pendingLabel : confirmLabel}
    </DialogButton>
  {/snippet}
</Modal>

<style>
  .confirm-content {
    display: grid;
    gap: var(--space-4);
  }

  .confirm-message {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    line-height: 1.45;
  }

  .confirm-hint {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1.45;
  }
</style>
