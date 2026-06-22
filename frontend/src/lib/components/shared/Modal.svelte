<script lang="ts">
  import XIcon from "@lucide/svelte/icons/x";
  import { onMount, untrack } from "svelte";
  import { pushModalFrame } from "@middleman/ui/stores/keyboard/modal-stack";
  import type { Snippet } from "svelte";

  // Shared in-app dialog shell: backdrop, dialog frame, header, footer slot,
  // focus trap, Escape close, focus restore, and optional modal-stack frame.
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

  let dialog: HTMLDivElement | null = $state(null);
  let previousFocus: HTMLElement | null = null;

  onMount(() => {
    function handleKey(event: KeyboardEvent): void {
      if (!open) return;
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key === "Tab") trapTab(event);
    }
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  });

  function focusables(): HTMLElement[] {
    if (!dialog) return [];
    const selector =
      "button:not([disabled]), [href], input:not([type='hidden']):not([disabled])," +
      " select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])";
    return Array.from(dialog.querySelectorAll<HTMLElement>(selector));
  }

  function trapTab(event: KeyboardEvent): void {
    const items = focusables();
    if (items.length === 0) {
      event.preventDefault();
      dialog?.focus();
      return;
    }
    const first = items[0]!;
    const last = items[items.length - 1]!;
    const active = document.activeElement as HTMLElement | null;
    if (event.shiftKey) {
      if (active === first || !dialog?.contains(active)) {
        event.preventDefault();
        last.focus();
      }
    } else if (active === last) {
      event.preventDefault();
      first.focus();
    }
  }

  $effect(() => {
    if (!open || !frameId) return;
    return untrack(() => pushModalFrame(frameId, []));
  });

  $effect(() => {
    if (open) {
      previousFocus = document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
      queueMicrotask(() => {
        const body = dialog?.querySelector<HTMLElement>(".dialog-body");
        const primary = body?.querySelector<HTMLElement>(
          "input:not([type='hidden']), textarea, [tabindex]:not([tabindex='-1'])",
        );
        // Prefer a body or footer control so confirmation dialogs land on their
        // first action (Cancel) instead of the header close button; only fall
        // back to the close button when there is nothing else to focus.
        const action = dialog?.querySelector<HTMLElement>(
          ".dialog-body button:not([disabled]), .dialog-body [tabindex]:not([tabindex='-1'])," +
            " .dialog-foot button:not([disabled]), .dialog-foot [tabindex]:not([tabindex='-1'])",
        );
        const close = dialog?.querySelector<HTMLElement>(".dialog-close");
        (primary ?? action ?? close ?? dialog)?.focus();
      });
    } else if (previousFocus) {
      const restore = previousFocus;
      previousFocus = null;
      queueMicrotask(() => restore.focus());
    }
  });

  function handleBackdropClick(event: MouseEvent): void {
    if (event.target === event.currentTarget) onClose();
  }
</script>

{#if open}
  <div
    class="backdrop"
    role="presentation"
    onclick={handleBackdropClick}
  >
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-label={ariaLabel ?? title}
      bind:this={dialog}
      tabindex="-1"
      style:max-width={`${width}px`}
    >
      {#if title}
        <header class="dialog-head">
          <h2 class="dialog-title">{title}</h2>
          {#if showClose}
            <button class="dialog-close" type="button" onclick={onClose} aria-label="Close">
              <XIcon size={14} strokeWidth={1.75} aria-hidden="true" />
            </button>
          {/if}
        </header>
      {/if}
      <div class="dialog-body">
        {@render children()}
      </div>
      {#if footer}
        <footer class="dialog-foot">
          {@render footer()}
        </footer>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(15, 17, 22, 0.4);
    z-index: 100;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 20px;
  }

  :root.dark .backdrop {
    background: rgba(0, 0, 0, 0.6);
  }

  .dialog {
    width: 100%;
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-lg);
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - 60px);
    overflow: hidden;
  }

  .dialog-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .dialog-title {
    font-size: var(--font-size-md);
    font-weight: 600;
    color: var(--text-primary);
  }

  .dialog-close {
    width: 22px;
    height: 22px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }

  .dialog-close:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .dialog-body {
    flex: 1;
    overflow: auto;
    padding: 14px;
  }

  .dialog-foot {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    padding: 10px 14px;
    border-top: 1px solid var(--border-default);
    background: var(--bg-surface);
  }
</style>
