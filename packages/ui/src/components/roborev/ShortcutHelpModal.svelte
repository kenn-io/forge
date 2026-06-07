<script lang="ts">
  import { untrack } from "svelte";

  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";

  interface Props {
    open: boolean;
    onclose: () => void;
  }
  let { open, onclose }: Props = $props();

  $effect(() => {
    if (!open) return;
    return untrack(() => pushModalFrame("roborev-shortcut-help", []));
  });

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === "Escape") {
      e.preventDefault();
      onclose();
    }
  }
</script>

{#if open}
  <div
    class="modal-backdrop"
    onclick={onclose}
    onkeydown={handleKeydown}
    role="dialog"
    aria-label="Keyboard Shortcuts"
    tabindex="-1"
  >
    <div
      class="modal-content"
      onclick={(e) => e.stopPropagation()}
      role="presentation"
    >
      <div class="modal-header">
        <h3>Keyboard Shortcuts</h3>
        <button class="close-btn" onclick={onclose}>
          X
        </button>
      </div>
      <div class="modal-body">
        <div class="shortcut-group">
          <h4>Table</h4>
          <dl>
            <div class="shortcut-row">
              <dt><kbd>j</kbd> / <kbd>k</kbd></dt>
              <dd>Move selection down / up</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>Enter</kbd></dt>
              <dd>Open drawer for selected row</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>x</kbd></dt>
              <dd>Cancel selected job</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>r</kbd></dt>
              <dd>Rerun selected job</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>h</kbd></dt>
              <dd>Toggle hide closed</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>/</kbd></dt>
              <dd>Focus search</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>?</kbd></dt>
              <dd>Toggle this help</dd>
            </div>
          </dl>
        </div>
        <div class="shortcut-group">
          <h4>Drawer</h4>
          <dl>
            <div class="shortcut-row">
              <dt><kbd>Esc</kbd></dt>
              <dd>Close drawer</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>a</kbd></dt>
              <dd>Toggle close / reopen review</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>c</kbd></dt>
              <dd>Focus comment input</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>l</kbd></dt>
              <dd>Switch to Log tab</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>p</kbd></dt>
              <dd>Switch to Prompt tab</dd>
            </div>
            <div class="shortcut-row">
              <dt><kbd>y</kbd></dt>
              <dd>Copy review output</dd>
            </div>
          </dl>
        </div>
      </div>
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed;
    inset: 0;
    z-index: 100;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--overlay-bg);
  }

  .modal-content {
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    max-width: 480px;
    width: 90%;
    max-height: 80vh;
    overflow-y: auto;
    box-shadow: var(--shadow-lg);
  }

  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-muted);
  }

  .modal-header h3 {
    margin: 0;
    font-size: var(--font-size-lg);
    font-weight: 600;
    color: var(--text-primary);
  }

  .close-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-weight: 600;
    cursor: pointer;
  }

  .close-btn:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .modal-body {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
    padding: 16px;
  }

  .shortcut-group h4 {
    margin: 0 0 8px;
    font-size: var(--font-size-sm);
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }

  .shortcut-group dl {
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .shortcut-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .shortcut-row dt {
    flex-shrink: 0;
    min-width: 72px;
    text-align: right;
  }

  .shortcut-row dd {
    margin: 0;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
  }

  kbd {
    display: inline-block;
    padding: 1px 5px;
    font-size: var(--font-size-xs);
    font-family: var(--font-mono);
    color: var(--text-primary);
    background: var(--bg-surface-hover);
    border: 1px solid var(--border-default);
    border-radius: 3px;
    line-height: 1.4;
  }
</style>
