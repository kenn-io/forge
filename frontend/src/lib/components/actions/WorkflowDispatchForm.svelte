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
  import type { Attachment } from "svelte/attachments";
  type Workflow = components["schemas"]["WorkflowDefinitionResponse"];
  type Environment = components["schemas"]["WorkflowEnvironmentResponse"];

  interface Props {
    workflow: Workflow;
    environments: readonly Environment[];
    initialRef: string;
    operation: OperationAvailability | undefined;
    state: WorkflowDispatchPresentationState;
    onsubmit: (request: WorkflowDispatchRequest) => void;
  }

  let { workflow, environments, initialRef, operation, state: presentation, onsubmit }: Props = $props();
  interface Draft {
    readonly ref: string;
    readonly values: Readonly<Record<string, unknown>>;
  }
  let drafts = $state<Record<string, Draft>>({});
  let submittedKeys = $state<Record<string, boolean>>({});
  let admissions = $state<Record<string, { blocked: boolean; ownerObserved: boolean }>>({});
  const draftKey = $derived(`${workflow.id}\u0000${workflow.definition_sha}\u0000${initialRef}`);
  const defaultDraft = $derived.by<Draft>(() => {
    workflow.id;
    workflow.definition_sha;
    return {
      ref: initialRef,
      values: Object.fromEntries(
        (workflow.inputs ?? []).map((input) => [
          input.name,
          input.has_default ? input.default : input.type === "boolean" ? false : "",
        ]),
      ),
    };
  });
  const draft = $derived(drafts[draftKey] ?? defaultDraft);
  const submitted = $derived(submittedKeys[draftKey] === true);
  const admitted = $derived(admissions[draftKey]?.blocked === true);
  const pending = $derived(presentation.kind === "pending");
  const controlsDisabled = $derived(pending || admitted);
  const explicitlyUnavailable = $derived(operation?.available === false || workflow.available === false);
  const unavailableReason = $derived.by(() => {
    if (operation?.available === false) return operation.unavailable_reason?.trim() || "Workflow dispatch is unavailable.";
    if (workflow.available === false) return workflow.unavailable_reason?.trim() || "This workflow is unavailable.";
    return "";
  });
  const errors = $derived(submitted ? validationErrors() : {});

  function inputControlId(name: string, index: number): string {
    return `workflow-input-${name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "input"}-${index}`;
  }

  function observePresentation(key: string, kind: WorkflowDispatchPresentationState["kind"]): Attachment {
    return () => {
      untrack(() => {
        const admission = admissions[key];
        if (!admission?.blocked) return;
        if (kind !== "idle") {
          admissions[key] = { blocked: true, ownerObserved: true };
        } else if (admission.ownerObserved) {
          delete admissions[key];
        }
      });
    };
  }

  function validationErrors(): Record<string, string> {
    const next: Record<string, string> = {};
    if (draft.ref.trim() === "") next.ref = "Git ref is required.";
    for (const input of workflow.inputs ?? []) {
      const value = draft.values[input.name];
      const missing = value === undefined || value === null || value === "" || (input.type === "string" && String(value).trim() === "");
      if (input.required && missing) next[input.name] = `${input.name} is required.`;
    }
    return next;
  }

  function submit(): void {
    if (pending || admitted || explicitlyUnavailable || presentation.kind !== "idle") return;
    submittedKeys[draftKey] = true;
    const currentErrors = validationErrors();
    if (Object.keys(currentErrors).length > 0) return;
    const normalized: Record<string, unknown> = {};
    for (const input of workflow.inputs ?? []) {
      const value = draft.values[input.name];
      if (!input.required && value === "") continue;
      normalized[input.name] = input.type === "string" ? String(value).trim() : value;
    }
    admissions[draftKey] = { blocked: true, ownerObserved: false };
    onsubmit({ ref: draft.ref.trim(), inputs: normalized });
  }
</script>

<div class="dispatch-root" {@attach observePresentation(draftKey, presentation.kind)}>
{#if presentation.kind === "conflict"}
  <p class="notice notice--error" role="alert">Workflow definition changed. Reload workflows before running it.</p>
{:else}
  <form class="dispatch-form" onsubmit={(event) => { event.preventDefault(); submit(); }}>
    <h2>{workflow.name}</h2>
    <label class="field">
      <span>Git ref <span aria-hidden="true">*</span></span>
      <input aria-label="Git ref" value={draft.ref} oninput={(event) => { drafts[draftKey] = { ...draft, ref: event.currentTarget.value }; }} disabled={controlsDisabled} aria-invalid={errors.ref ? "true" : undefined} aria-describedby={errors.ref ? "workflow-ref-error" : undefined} />
    </label>
    {#if errors.ref}<p id="workflow-ref-error" class="field-error" role="alert">{errors.ref}</p>{/if}

    {#each workflow.inputs ?? [] as input, index (input.name)}
      <div class="field">
        {#if input.type === "boolean"}
          <Checkbox label={input.name} checked={draft.values[input.name] === true} disabled={controlsDisabled} onchange={(checked) => { drafts[draftKey] = { ...draft, values: { ...draft.values, [input.name]: checked } }; }} />
        {:else}
          <label for={inputControlId(input.name, index)}>{input.name}{#if input.required} <span aria-hidden="true">*</span>{/if}</label>
          {#if input.type === "choice" || input.type === "environment"}
            <select
              id={inputControlId(input.name, index)}
              aria-label={input.name}
              disabled={controlsDisabled}
              value={String(draft.values[input.name] ?? "")}
              onchange={(event) => { drafts[draftKey] = { ...draft, values: { ...draft.values, [input.name]: event.currentTarget.value } }; }}
              aria-invalid={errors[input.name] ? "true" : undefined}
              aria-describedby={errors[input.name] ? `${inputControlId(input.name, index)}-error` : undefined}
            >
              {#if input.type === "environment"}<option value="">Select an environment</option>{/if}
              {#each input.type === "choice" ? (input.options ?? []) : environments.map((item) => item.name) as option (option)}<option value={option}>{option}</option>{/each}
            </select>
          {:else}
            <input
              id={inputControlId(input.name, index)}
              aria-label={input.name}
              type={input.type === "number" ? "number" : "text"}
              disabled={controlsDisabled}
              value={String(draft.values[input.name] ?? "")}
              oninput={(event) => { const raw = event.currentTarget.value; drafts[draftKey] = { ...draft, values: { ...draft.values, [input.name]: input.type === "number" ? (raw === "" ? "" : Number(raw)) : raw } }; }}
              aria-invalid={errors[input.name] ? "true" : undefined}
              aria-describedby={errors[input.name] ? `${inputControlId(input.name, index)}-error` : undefined}
            />
          {/if}
        {/if}
        {#if input.description}<small>{input.description}</small>{/if}
        {#if errors[input.name]}<p id={`${inputControlId(input.name, index)}-error`} class="field-error" role="alert">{errors[input.name]}</p>{/if}
      </div>
    {/each}

    {#if unavailableReason}<p class="notice notice--error" role="alert">{unavailableReason}</p>{/if}
    {#if presentation.kind === "uncertain"}<p class="notice notice--error" role="alert">{presentation.message}</p>{/if}
    <Button type="submit" tone="primary" disabled={controlsDisabled || explicitlyUnavailable || presentation.kind !== "idle"}>{pending || admitted ? "Running workflow…" : "Run workflow"}</Button>
  </form>
{/if}
</div>

<style>
  .dispatch-root { display: contents; }
  .dispatch-form, .field { display: grid; gap: var(--space-2); }
  .dispatch-form { gap: var(--space-4); }
  h2 { margin: 0; font-size: var(--font-size-md); color: var(--text-primary); }
  label, small { font-size: var(--font-size-sm); color: var(--text-secondary); }
  input, select { box-sizing: border-box; width: 100%; min-height: 32px; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-inset); color: var(--text-primary); padding: 0 var(--space-3); font: inherit; }
  input:focus, select:focus { outline: 2px solid var(--focus-ring); outline-offset: 1px; }
  .field-error, .notice { margin: 0; font-size: var(--font-size-sm); }
  .field-error, .notice--error { color: var(--status-danger-text, var(--text-danger)); }
</style>
