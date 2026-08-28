<script lang="ts">
  import { tick } from "svelte";
  import type { Attachment } from "svelte/attachments";
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
    onreload: () => void;
  }

  let { open, workflow, environments, initialRef, operation, state: presentation, trigger = null, onsubmit, onclose, onreload }: Props = $props();
  let reloadRequested = $state(false);
  const canDismiss = $derived(presentation.kind === "idle" || presentation.kind === "succeeded");

  async function close(): Promise<void> {
    if (!canDismiss) return;
    onclose();
    await tick();
    trigger?.focus();
  }

  function reload(): void {
    if (reloadRequested || presentation.kind !== "conflict") return;
    reloadRequested = true;
    onreload();
  }

  function reloadCycle(kind: WorkflowDispatchPresentationState["kind"]): Attachment {
    return () => {
      if (kind !== "conflict") reloadRequested = false;
      return () => {
        reloadRequested = false;
      };
    };
  }
</script>

<Modal {open} title="Run workflow" width={520} frameId="workflow-dispatch" onClose={() => { void close(); }}>
  <div class="dispatch-dialog-body" {@attach reloadCycle(presentation.kind)}>
    <WorkflowDispatchForm {workflow} {environments} {initialRef} {operation} state={presentation} {onsubmit} />
  </div>
  {#snippet footer()}
    {#if presentation.kind === "idle"}<DialogButton onclick={() => { void close(); }}>Cancel</DialogButton>{/if}
    {#if presentation.kind === "conflict"}<DialogButton tone="primary" disabled={reloadRequested} onclick={reload}>Reload workflows</DialogButton>{/if}
    {#if presentation.kind === "succeeded"}<DialogButton tone="primary" onclick={() => { void close(); }}>Close</DialogButton>{/if}
  {/snippet}
</Modal>

<style>
  .dispatch-dialog-body { display: contents; }
</style>
