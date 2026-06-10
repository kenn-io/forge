<script lang="ts">
  import Modal from "../shared/Modal.svelte";
  import type { KataRecurrence } from "../../api/kata/taskTypes";

  interface Props {
    open: boolean;
    recurrence: KataRecurrence;
    onConfirm: () => void;
    onCancel: () => void;
  }

  let { open, recurrence, onConfirm, onCancel }: Props = $props();
</script>

<Modal {open} title="Delete recurrence" onClose={onCancel} width={420}>
  <p class="body">
    Stop creating new occurrences of
    <strong>{recurrence.template_title}</strong>?
    Existing open issues are not affected.
  </p>
  {#snippet footer()}
    <button type="button" class="btn-secondary" onclick={onCancel}>Cancel</button>
    <button type="button" class="btn-destructive" onclick={onConfirm}>Delete</button>
  {/snippet}
</Modal>

<style>
  .body {
    color: var(--text-primary);
    font-size: var(--font-size-md);
    line-height: 1.45;
  }

  .btn-secondary {
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
  }

  .btn-destructive {
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--accent-red, #c14a3c);
    background: var(--accent-red, #c14a3c);
    color: white;
  }

  .btn-secondary:hover { background: var(--bg-surface-hover); }
  .btn-destructive:hover { filter: brightness(1.08); }
</style>
