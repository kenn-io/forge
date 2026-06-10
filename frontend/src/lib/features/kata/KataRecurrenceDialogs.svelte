<script lang="ts">
  import RecurrenceDeleteDialog from "../../components/recurrence/RecurrenceDeleteDialog.svelte";
  import RecurrenceEditorDialog from "../../components/recurrence/RecurrenceEditorDialog.svelte";
  import type {
    KataCreateRecurrenceInput,
    KataPatchRecurrenceInput,
    KataRecurrence,
    KataTaskDetail,
  } from "../../api/kata/taskTypes.js";

  interface Props {
    selectedIssue: KataTaskDetail | null;
    actor: string;
    onCreate: (projectID: number, input: KataCreateRecurrenceInput) => Promise<void>;
    onPatch: (id: number, input: KataPatchRecurrenceInput, etag: string) => Promise<void>;
    onDelete: (recurrence: KataRecurrence) => Promise<boolean>;
  }

  let { selectedIssue, actor, onCreate, onPatch, onDelete }: Props = $props();

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

  export function openCreateRecurrence(): void {
    recurrenceDialog = { open: true, mode: "create", recurrence: null, etag: "" };
  }

  export function openEditRecurrence(recurrence: KataRecurrence): void {
    recurrenceDialog = {
      open: true,
      mode: "edit",
      recurrence,
      etag: `"rev-${recurrence.revision}"`,
    };
  }

  export function openDeleteRecurrence(recurrence: KataRecurrence): void {
    recurrenceDelete = { open: true, recurrence };
  }

  export function closeAll(): void {
    closeRecurrenceDialog();
    closeDeleteRecurrence();
  }

  function closeRecurrenceDialog(): void {
    recurrenceDialog = { open: false, mode: "create", recurrence: null, etag: "" };
  }

  function closeDeleteRecurrence(): void {
    if (deletingRecurrence) return;
    recurrenceDelete = { open: false, recurrence: null };
  }

  async function confirmDeleteRecurrence(): Promise<void> {
    const recurrence = recurrenceDelete.recurrence;
    if (!recurrence || deletingRecurrence) return;
    deletingRecurrence = true;
    try {
      const ok = await onDelete(recurrence);
      if (ok) {
        recurrenceDelete = { open: false, recurrence: null };
      }
    } finally {
      deletingRecurrence = false;
    }
  }
</script>

{#if selectedIssue && recurrenceDialog.open}
  <RecurrenceEditorDialog
    open={recurrenceDialog.open}
    mode={recurrenceDialog.mode === "create"
      ? { kind: "create", projectID: selectedIssue.issue.project_id }
      : { kind: "edit", recurrence: recurrenceDialog.recurrence, etag: recurrenceDialog.etag }}
    {actor}
    onClose={closeRecurrenceDialog}
    onCreate={onCreate}
    onPatch={onPatch}
  />
{/if}

{#if recurrenceDelete.open && recurrenceDelete.recurrence}
  <RecurrenceDeleteDialog
    open={recurrenceDelete.open}
    recurrence={recurrenceDelete.recurrence}
    onConfirm={() => {
      void confirmDeleteRecurrence();
    }}
    onCancel={closeDeleteRecurrence}
  />
{/if}
