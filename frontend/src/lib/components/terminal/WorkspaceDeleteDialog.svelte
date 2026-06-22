<script lang="ts">
  import { tick, untrack } from "svelte";
  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import { AlertIcon } from "../../icons.ts";

  interface Props {
    open: boolean;
    title: string;
    message: string;
    hint: string;
    confirmLabel: string;
    pendingLabel?: string | undefined;
    busy?: boolean;
    frameId?: string | undefined;
    onCancel: () => void;
    onConfirm: () => void;
  }

  let {
    open,
    title,
    message,
    hint,
    confirmLabel,
    pendingLabel = confirmLabel,
    busy = false,
    frameId = undefined,
    onCancel,
    onConfirm,
  }: Props = $props();

  let cancelButtonEl = $state<HTMLButtonElement | null>(null);

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") {
      event.preventDefault();
      onCancel();
      return;
    }
    if (event.key !== "Tab") return;
    const container = event.currentTarget;
    if (!(container instanceof HTMLElement)) return;
    const dialog = container.querySelector("[role='dialog']");
    if (!(dialog instanceof HTMLElement)) return;
    const focusable = Array.from(
      dialog.querySelectorAll<HTMLElement>(
        "button:not(:disabled), input:not(:disabled), [tabindex]:not([tabindex='-1'])",
      ),
    );
    if (focusable.length === 0) return;
    const currentIndex = focusable.findIndex(
      (el) => el === document.activeElement,
    );
    const nextIndex = event.shiftKey
      ? currentIndex <= 0
        ? focusable.length - 1
        : currentIndex - 1
      : currentIndex < 0 || currentIndex >= focusable.length - 1
        ? 0
        : currentIndex + 1;
    event.preventDefault();
    focusable[nextIndex]?.focus();
  }

  $effect(() => {
    if (!open) return;
    void tick().then(() => cancelButtonEl?.focus());
  });

  $effect(() => {
    if (!open || !frameId) return;
    return untrack(() => pushModalFrame(frameId, []));
  });
</script>

{#if open}
  <div
    class="force-delete-backdrop"
    role="presentation"
    onkeydown={handleKeydown}
  >
    <div
      class="force-delete-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="workspace-delete-title"
      aria-describedby="workspace-delete-message"
    >
      <div class="force-delete-header">
        <AlertIcon
          class="force-delete-icon"
          size="20"
          strokeWidth="2"
          aria-hidden="true"
        />
        <h2 id="workspace-delete-title">{title}</h2>
      </div>
      <p id="workspace-delete-message" class="force-delete-message">
        {message}
      </p>
      <p class="force-delete-hint">
        {hint}
      </p>
      <div class="force-delete-actions">
        <button
          type="button"
          class="force-delete-cancel"
          disabled={busy}
          bind:this={cancelButtonEl}
          onclick={onCancel}
        >
          Cancel
        </button>
        <button
          type="button"
          class="force-delete-confirm"
          disabled={busy}
          onclick={onConfirm}
        >
          {busy ? pendingLabel : confirmLabel}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .force-delete-backdrop {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    background: color-mix(in srgb, black 50%, transparent);
    backdrop-filter: blur(2px);
    animation: force-delete-fade 120ms ease-out;
  }

  .force-delete-dialog {
    width: min(420px, 100%);
    background: var(--bg-surface);
    color: var(--text-primary);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    box-shadow: 0 24px 80px rgb(0 0 0 / 35%);
    padding: 20px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    animation: force-delete-pop 160ms cubic-bezier(0.16, 1, 0.3, 1);
  }

  .force-delete-header {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  :global(.force-delete-icon) {
    color: var(--accent-red);
    flex-shrink: 0;
  }

  .force-delete-header h2 {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 600;
    color: var(--text-primary);
  }

  .force-delete-message {
    margin: 0;
    font-size: var(--font-size-md);
    color: var(--text-secondary);
    line-height: 1.5;
  }

  .force-delete-hint {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-muted);
    line-height: 1.5;
  }

  .force-delete-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }

  .force-delete-cancel,
  .force-delete-confirm {
    height: 30px;
    padding: 0 14px;
    font-size: var(--font-size-sm);
    font-weight: 500;
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition: background-color 80ms ease, color 80ms ease,
      border-color 80ms ease;
  }

  .force-delete-cancel {
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    color: var(--text-secondary);
  }

  .force-delete-cancel:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .force-delete-confirm {
    background: var(--accent-red);
    border: 1px solid var(--accent-red);
    color: #fff;
    font-weight: 600;
  }

  .force-delete-confirm:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent-red) 88%, black);
    border-color: color-mix(in srgb, var(--accent-red) 88%, black);
  }

  .force-delete-cancel:disabled,
  .force-delete-confirm:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  @keyframes force-delete-fade {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  @keyframes force-delete-pop {
    from {
      opacity: 0;
      transform: translateY(8px) scale(0.98);
    }
    to {
      opacity: 1;
      transform: translateY(0) scale(1);
    }
  }
</style>
