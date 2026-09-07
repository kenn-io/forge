<script lang="ts">
  import { tick } from "svelte";
  import type { WorkflowDefinitionResponse } from "../../api/generated/models/workflowDefinitionResponse.js";
  import type { WorkflowEnvironmentResponse } from "../../api/generated/models/workflowEnvironmentResponse.js";
  import type { OperationAvailability } from "../../api/types.js";
  import DialogButton from "../shared/DialogButton.svelte";
  import Modal from "../shared/Modal.svelte";
  import WorkflowDispatchForm, { type WorkflowDispatchRequest } from "./WorkflowDispatchForm.svelte";
  import type { WorkflowDispatchPresentationState } from "./workflow-dispatch-presentation.js";

  type Workflow = WorkflowDefinitionResponse;
  type Environment = WorkflowEnvironmentResponse;

  interface Props {
    open: boolean;
    workflow: Workflow;
    environments: readonly Environment[];
    initialRef: string;
    operation: OperationAvailability | undefined;
    state: WorkflowDispatchPresentationState;
    trigger?: HTMLElement | null;
    reloading?: boolean;
    onsubmit: (request: WorkflowDispatchRequest) => void;
    onclose: () => void;
    onreload: () => void;
    onnewcycle: () => void;
  }

  let { open, workflow, environments, initialRef, operation, state: presentation, trigger = null, reloading = false, onsubmit, onclose, onreload, onnewcycle }: Props = $props();

  async function close(): Promise<void> {
    onclose();
    await tick();
    trigger?.focus();
  }
</script>

<Modal {open} title="Run workflow" width={520} frameId="workflow-dispatch" onClose={() => { void close(); }}>
  <WorkflowDispatchForm {workflow} {environments} {initialRef} {operation} state={presentation} {onsubmit} />
  {#snippet footer()}
    <DialogButton onclick={() => { void close(); }}>{presentation.kind === "idle" ? "Cancel" : "Close"}</DialogButton>
    {#if presentation.kind === "conflict"}
      <DialogButton tone="primary" disabled={reloading} onclick={onreload}>{reloading ? "Reloading workflows…" : "Reload workflows"}</DialogButton>
    {/if}
    {#if presentation.kind === "succeeded" || presentation.kind === "failed"}
      <DialogButton tone="primary" onclick={onnewcycle}>Run again</DialogButton>
    {/if}
    {#if presentation.kind === "uncertain"}
      <DialogButton tone="primary" onclick={onnewcycle}>Dispatch again</DialogButton>
    {/if}
  {/snippet}
</Modal>
