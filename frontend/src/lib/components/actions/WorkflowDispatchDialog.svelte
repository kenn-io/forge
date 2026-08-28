<script lang="ts">
  import { tick } from "svelte";
  import type { components } from "../../api/generated/schema.js";
  import type { OperationAvailability } from "../../api/types.js";
  import DialogButton from "../shared/DialogButton.svelte";
  import Modal from "../shared/Modal.svelte";
  import WorkflowDispatchForm, { type WorkflowDispatchPresentationState, type WorkflowDispatchRequest } from "./WorkflowDispatchForm.svelte";

  type Workflow = components["schemas"]["WorkflowDefinitionResponse"];
  type Environment = components["schemas"]["WorkflowEnvironmentResponse"];

  interface Props {
    open: boolean;
    workflow: Workflow;
    environments: readonly Environment[];
    initialRef: string;
    operation: OperationAvailability | undefined;
    state: WorkflowDispatchPresentationState;
    trigger?: HTMLElement | null;
    onsubmit: (request: WorkflowDispatchRequest) => void;
    onclose: () => void;
    onreload?: (() => void) | undefined;
  }

  let { open, workflow, environments, initialRef, operation, state, trigger = null, onsubmit, onclose, onreload = undefined }: Props = $props();
  const canDismiss = $derived(state.kind === "idle" || state.kind === "succeeded");

  async function close(): Promise<void> {
    if (!canDismiss) return;
    onclose();
    await tick();
    trigger?.focus();
  }
</script>

<Modal {open} title="Run workflow" width={520} frameId="workflow-dispatch" onClose={() => { void close(); }}>
  <WorkflowDispatchForm {workflow} {environments} {initialRef} {operation} {state} {onsubmit} />
  {#snippet footer()}
    {#if state.kind === "idle"}<DialogButton onclick={() => { void close(); }}>Cancel</DialogButton>{/if}
    {#if state.kind === "conflict"}<DialogButton tone="primary" onclick={() => onreload?.()}>Reload workflows</DialogButton>{/if}
    {#if state.kind === "succeeded"}<DialogButton tone="primary" onclick={() => { void close(); }}>Close</DialogButton>{/if}
  {/snippet}
</Modal>
