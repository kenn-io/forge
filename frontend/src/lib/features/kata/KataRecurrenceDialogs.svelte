<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy } from "svelte";
  import RecurrenceDeleteDialog from "../../components/recurrence/RecurrenceDeleteDialog.svelte";
  import RecurrenceEditorDialog from "../../components/recurrence/RecurrenceEditorDialog.svelte";
  import type {
    KataCreateRecurrenceInput,
    KataPatchRecurrenceInput,
    KataRecurrence,
    KataTaskDetail,
  } from "../../api/kata/taskTypes.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { KataCommand } from "./kata-command.js";

  interface Props {
    selectedIssue: KataTaskDetail | null;
    actor: string;
    disabled?: boolean | undefined;
    onCreate: (projectID: number, input: KataCreateRecurrenceInput) => KataCommand<void, unknown>;
    onPatch: (id: number, input: KataPatchRecurrenceInput, etag: string) => KataCommand<void, unknown>;
    onDelete: (recurrence: KataRecurrence) => KataCommand<boolean>;
  }

  let { selectedIssue, actor, disabled = false, onCreate, onPatch, onDelete }: Props = $props();

  const runtime = getAppRuntime();

  let recurrenceDialog = $state<
    | { open: false; mode: "create"; recurrence: null; etag: "" }
    | { open: true; mode: "create"; recurrence: null; etag: "" }
    | { open: true; mode: "edit"; recurrence: KataRecurrence; etag: string }
  >({ open: false, mode: "create", recurrence: null, etag: "" });
  let recurrenceDelete = $state<{ open: boolean; recurrence: KataRecurrence | null }>({
    open: false,
    recurrence: null,
  });
  let deletingRecurrence = $state(false);
  let deleteExecution: AppExecution<void, never> | null = null;

  export function openCreateRecurrence(): void {
    if (disabled) return;
    recurrenceDialog = { open: true, mode: "create", recurrence: null, etag: "" };
  }

  export function openEditRecurrence(recurrence: KataRecurrence): void {
    if (disabled) return;
    recurrenceDialog = {
      open: true,
      mode: "edit",
      recurrence,
      etag: `"rev-${recurrence.revision}"`,
    };
  }

  export function openDeleteRecurrence(recurrence: KataRecurrence): void {
    if (disabled) return;
    recurrenceDelete = { open: true, recurrence };
  }

  export function closeAll(): void {
    closeRecurrenceDialog();
    closeDeleteRecurrence();
  }

  // A delete conflict (412) reloads the recurrence list; without reconciling,
  // the open delete dialog would retry with the stale revision forever.
  export function reconcileRecurrences(recurrences: readonly KataRecurrence[]): void {
    const target = recurrenceDelete.recurrence;
    if (!recurrenceDelete.open || !target) return;
    const fresh = recurrences.find((item) => item.uid === target.uid);
    if (!fresh) {
      recurrenceDelete = { open: false, recurrence: null };
      return;
    }
    if (fresh.revision !== target.revision) {
      recurrenceDelete = { open: true, recurrence: fresh };
    }
  }

  function closeRecurrenceDialog(): void {
    recurrenceDialog = { open: false, mode: "create", recurrence: null, etag: "" };
  }

  function closeDeleteRecurrence(): void {
    if (deletingRecurrence) return;
    recurrenceDelete = { open: false, recurrence: null };
  }

  function confirmDeleteRecurrence(): void {
    const recurrence = recurrenceDelete.recurrence;
    if (disabled || !recurrence || deletingRecurrence) return;
    deletingRecurrence = true;
    deleteExecution = runtime.runCommand(
      onDelete(recurrence).pipe(
        Effect.tap((ok) =>
          Effect.sync(() => {
            if (ok) recurrenceDelete = { open: false, recurrence: null };
          }),
        ),
        Effect.ensuring(Effect.sync(() => (deletingRecurrence = false))),
        Effect.asVoid,
      ),
      {
        operation: "delete Kata recurrence",
        safeContext: { recurrenceUid: recurrence.uid },
        onFailure: () => {},
      },
    );
  }

  onDestroy(() => deleteExecution?.interrupt());
</script>

{#if selectedIssue && recurrenceDialog.open}
  <RecurrenceEditorDialog
    open={recurrenceDialog.open}
    mode={recurrenceDialog.mode === "create"
      ? { kind: "create", projectID: selectedIssue.issue.project_id }
      : { kind: "edit", recurrence: recurrenceDialog.recurrence, etag: recurrenceDialog.etag }}
    {actor}
    {disabled}
    onClose={closeRecurrenceDialog}
    onCreate={onCreate}
    onPatch={onPatch}
  />
{/if}

{#if recurrenceDelete.open && recurrenceDelete.recurrence}
  <RecurrenceDeleteDialog
    open={recurrenceDelete.open}
    recurrence={recurrenceDelete.recurrence}
    {disabled}
    onConfirm={() => {
      confirmDeleteRecurrence();
    }}
    onCancel={closeDeleteRecurrence}
  />
{/if}
