<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy } from "svelte";
  import Modal from "../shared/Modal.svelte";
  import RecurrenceEditor from "./RecurrenceEditor.svelte";
  import type {
    KataCreateRecurrenceInput,
    KataPatchRecurrenceInput,
    KataRecurrence,
  } from "../../api/kata/taskTypes";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { KataCommand } from "../../features/kata/kata-command.js";

  type Mode =
    | { kind: "create"; projectID: number }
    | { kind: "edit"; recurrence: KataRecurrence; etag: string };

  interface Props {
    open: boolean;
    mode: Mode;
    actor: string;
    disabled?: boolean | undefined;
    onClose: () => void;
    onCreate: (projectID: number, input: KataCreateRecurrenceInput) => KataCommand<void, unknown>;
    onPatch: (id: number, input: KataPatchRecurrenceInput, etag: string) => KataCommand<void, unknown>;
  }

  let { open, mode, actor, disabled = false, onClose, onCreate, onPatch }: Props = $props();

  const runtime = getAppRuntime();

  let busy = $state(false);
  let editorRef: { trySave: () => KataCommand<void>; canSave: () => boolean } | null = $state(null);
  let saveExecution: AppExecution<void, never> | null = null;

  function handleSave(): void {
    if (disabled || !editorRef) return;
    if (!editorRef.canSave()) return;
    busy = true;
    saveExecution = runtime.runCommand(
      editorRef.trySave().pipe(Effect.ensuring(Effect.sync(() => (busy = false)))),
      {
        operation: "save Kata recurrence",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  onDestroy(() => saveExecution?.interrupt());
</script>

<Modal
  {open}
  title={mode.kind === "create" ? "New recurrence" : "Edit recurrence"}
  onClose={busy ? () => {} : onClose}
  width={560}
>
  <RecurrenceEditor
    bind:this={editorRef}
    {mode}
    {actor}
    {onCreate}
    {onPatch}
    onSaved={onClose}
  />
  {#snippet footer()}
    <button
      type="button"
      class="btn-secondary"
      disabled={busy}
      onclick={onClose}
    >Cancel</button>
    <button
      type="button"
      class="btn-primary"
      disabled={disabled || busy || !editorRef?.canSave()}
      onclick={handleSave}
    >{busy ? "Saving..." : "Save"}</button>
  {/snippet}
</Modal>

<style>
  .btn-secondary,
  .btn-primary {
    padding: 6px 12px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
  }

  .btn-primary {
    background: var(--accent-primary);
    border-color: transparent;
    color: white;
  }

  .btn-primary:disabled,
  .btn-secondary:disabled {
    opacity: 0.55;
    pointer-events: none;
  }
</style>
