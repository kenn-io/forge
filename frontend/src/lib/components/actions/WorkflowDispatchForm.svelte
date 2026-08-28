<script module lang="ts">
  export type WorkflowDispatchPresentationState =
    | { readonly kind: "idle" }
    | { readonly kind: "pending" }
    | { readonly kind: "succeeded" }
    | { readonly kind: "uncertain"; readonly message: string }
    | { readonly kind: "conflict" };

  export interface WorkflowDispatchRequest {
    readonly ref: string;
    readonly inputs: Readonly<Record<string, unknown>>;
  }
</script>

<script lang="ts">
  import { Button, Checkbox } from "@kenn-io/kit-ui";
  import type { components } from "../../api/generated/schema.js";
  import type { OperationAvailability } from "../../api/types.js";
  import { untrack } from "svelte";
  type Workflow = components["schemas"]["WorkflowDefinitionResponse"];
  type Environment = components["schemas"]["WorkflowEnvironmentResponse"];
  type Input = components["schemas"]["WorkflowInputResponse"];

  interface Props {
    workflow: Workflow;
    environments: readonly Environment[];
    initialRef: string;
    operation: OperationAvailability | undefined;
    state: WorkflowDispatchPresentationState;
    onsubmit: (request: WorkflowDispatchRequest) => void;
  }

  let { workflow, environments, initialRef, operation, state: presentation, onsubmit }: Props = $props();
  let ref = $state(untrack(() => initialRef));
  let values = $state<Record<string, unknown>>(initialValues(untrack(() => workflow.inputs ?? [])));
  let submitted = $state(false);

  const pending = $derived(presentation.kind === "pending");
  const unavailableReason = $derived(operation?.available === false ? (operation.unavailable_reason ?? "Workflow dispatch is unavailable.") : workflow.available ? "" : (workflow.unavailable_reason ?? "This workflow is unavailable."));
  const errors = $derived.by(() => {
    if (!submitted) return {} as Record<string, string>;
    const next: Record<string, string> = {};
    if (ref.trim() === "") next.ref = "Git ref is required.";
    for (const input of workflow.inputs ?? []) {
      const value = values[input.name];
      if (input.required && (value === "" || value === undefined || value === null)) next[input.name] = `${input.name} is required.`;
    }
    return next;
  });

  function initialValues(inputs: readonly Input[]): Record<string, unknown> {
    return Object.fromEntries(inputs.map((input) => [input.name, input.has_default ? input.default : input.type === "boolean" ? false : ""]));
  }

  function submit(): void {
    if (pending || unavailableReason !== "" || presentation.kind !== "idle") return;
    submitted = true;
    const normalized: Record<string, unknown> = {};
    for (const input of workflow.inputs ?? []) {
      const value = values[input.name];
      if (input.required && (value === "" || value === undefined || value === null)) continue;
      if (!input.required && value === "") continue;
      normalized[input.name] = input.type === "string" ? String(value).trim() : value;
    }
    if (ref.trim() === "" || Object.keys(errors).length > 0) return;
    onsubmit({ ref: ref.trim(), inputs: normalized });
  }
</script>

{#if presentation.kind === "conflict"}
  <p class="notice notice--error" role="alert">Workflow definition changed. Reload workflows before running it.</p>
{:else}
  <form class="dispatch-form" onsubmit={(event) => { event.preventDefault(); submit(); }}>
    <h2>{workflow.name}</h2>
    <label class="field">
      <span>Git ref <span aria-hidden="true">*</span></span>
      <input aria-label="Git ref" bind:value={ref} disabled={pending} aria-invalid={errors.ref ? "true" : undefined} aria-describedby={errors.ref ? "workflow-ref-error" : undefined} />
    </label>
    {#if errors.ref}<p id="workflow-ref-error" class="field-error" role="alert">{errors.ref}</p>{/if}

    {#each workflow.inputs ?? [] as input (input.name)}
      <div class="field">
        {#if input.type === "boolean"}
          <Checkbox label={input.name} checked={values[input.name] === true} disabled={pending} onchange={(checked) => { values[input.name] = checked; }} />
        {:else}
          <label for={`workflow-input-${input.name}`}>{input.name}{#if input.required} <span aria-hidden="true">*</span>{/if}</label>
          {#if input.type === "choice" || input.type === "environment"}
            <select id={`workflow-input-${input.name}`} aria-label={input.name} disabled={pending} bind:value={values[input.name]} aria-invalid={errors[input.name] ? "true" : undefined}>
              {#if input.type === "environment"}<option value="">Select an environment</option>{/if}
              {#each input.type === "choice" ? (input.options ?? []) : environments.map((item) => item.name) as option (option)}<option value={option}>{option}</option>{/each}
            </select>
          {:else}
            <input id={`workflow-input-${input.name}`} aria-label={input.name} type={input.type === "number" ? "number" : "text"} disabled={pending} value={String(values[input.name] ?? "")} oninput={(event) => { const raw = event.currentTarget.value; values[input.name] = input.type === "number" ? (raw === "" ? "" : Number(raw)) : raw; }} aria-invalid={errors[input.name] ? "true" : undefined} />
          {/if}
        {/if}
        {#if input.description}<small>{input.description}</small>{/if}
        {#if errors[input.name]}<p class="field-error" role="alert">{errors[input.name]}</p>{/if}
      </div>
    {/each}

    {#if unavailableReason}<p class="notice notice--error" role="alert">{unavailableReason}</p>{/if}
    {#if presentation.kind === "uncertain"}<p class="notice notice--error" role="alert">{presentation.message}</p>{/if}
    <Button type="submit" tone="primary" disabled={pending || unavailableReason !== "" || presentation.kind !== "idle"}>{pending ? "Running workflow…" : "Run workflow"}</Button>
  </form>
{/if}

<style>
  .dispatch-form, .field { display: grid; gap: var(--space-2); }
  .dispatch-form { gap: var(--space-4); }
  h2 { margin: 0; font-size: var(--font-size-md); color: var(--text-primary); }
  label, small { font-size: var(--font-size-sm); color: var(--text-secondary); }
  input, select { box-sizing: border-box; width: 100%; min-height: 32px; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-inset); color: var(--text-primary); padding: 0 var(--space-3); font: inherit; }
  input:focus, select:focus { outline: 2px solid var(--focus-ring); outline-offset: 1px; }
  .field-error, .notice { margin: 0; font-size: var(--font-size-sm); }
  .field-error, .notice--error { color: var(--status-danger-text, var(--text-danger)); }
</style>
