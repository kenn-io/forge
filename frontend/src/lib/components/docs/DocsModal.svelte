<script lang="ts">
  import { untrack, type Snippet } from "svelte";
  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";

  interface Props {
    open: boolean;
    title: string;
    width?: number;
    onClose: () => void;
    children?: Snippet;
    footer?: Snippet;
  }

  let {
    open,
    title,
    width = 520,
    onClose,
    children,
    footer,
  }: Props = $props();

  $effect(() => {
    if (!open) return;
    return untrack(() => pushModalFrame(`docs-modal:${title}`, []));
  });

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key === "Escape") {
      onClose();
      return;
    }
    if (event.key !== "Tab") return;
    const container = event.currentTarget;
    if (!(container instanceof HTMLElement)) return;
    const dialog = container.querySelector("[role='dialog']");
    if (!(dialog instanceof HTMLElement)) return;
    const focusable = Array.from(
      dialog.querySelectorAll<HTMLElement>(
        "button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled), [tabindex]:not([tabindex='-1'])",
      ),
    );
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function handleBackdropClick(event: MouseEvent): void {
    if (event.target === event.currentTarget) {
      onClose();
    }
  }
</script>

{#if open}
  <div
    class="modal-backdrop"
    role="presentation"
    onkeydown={handleKeydown}
    onclick={handleBackdropClick}
  >
    <div
      class="docs-modal"
      style:--modal-width={`${width}px`}
      role="dialog"
      aria-modal="true"
      aria-labelledby="docs-modal-title"
      tabindex="-1"
    >
      <header class="modal-header">
        <h2 id="docs-modal-title">{title}</h2>
        <button type="button" class="close-btn" aria-label="Close" onclick={onClose}>x</button>
      </header>
      <div class="modal-body">
        {@render children?.()}
      </div>
      {#if footer}
        <footer class="modal-footer">
          {@render footer()}
        </footer>
      {/if}
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed;
    inset: 0;
    z-index: 60;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
    background: rgba(15, 23, 42, 0.36);
  }

  .docs-modal {
    width: min(var(--modal-width), 100%);
    max-height: min(86vh, 820px);
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-primary);
    color: var(--text-primary);
    box-shadow: 0 18px 45px rgba(15, 23, 42, 0.22);
    overflow: hidden;
  }

  .modal-header {
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border-default);
  }

  .modal-header h2 {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 650;
    line-height: 1.2;
  }

  .close-btn {
    width: 28px;
    height: 28px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .close-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .modal-body {
    min-height: 0;
    overflow: auto;
    padding: 16px;
  }

  .modal-footer {
    flex: 0 0 auto;
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 12px 16px;
    border-top: 1px solid var(--border-default);
    background: var(--bg-surface);
  }
</style>
