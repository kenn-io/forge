<script module lang="ts">
  import type { components as GeneratedComponents } from "../../api/generated/schema.js";

  type WorkflowRun = GeneratedComponents["schemas"]["WorkflowRunResponse"];

  export type WorkflowDispatchPresentationState =
    | { readonly kind: "idle" }
    | { readonly kind: "pending" }
    | { readonly kind: "locating" }
    | { readonly kind: "succeeded"; readonly run?: WorkflowRun; readonly message?: string }
    | { readonly kind: "failed"; readonly message: string }
    | {
        readonly kind: "uncertain";
        readonly message: string;
        readonly candidates: readonly WorkflowRun[];
      }
    | { readonly kind: "conflict" };

  export interface WorkflowDispatchRequest {
    readonly ref: string;
    readonly inputs: Readonly<Record<string, unknown>>;
  }
</script>

<script lang="ts">
  import { Button, Checkbox, SelectDropdown, type SelectDropdownOption } from "@kenn-io/kit-ui";
  import type { components } from "../../api/generated/schema.js";
  import type { OperationAvailability } from "../../api/types.js";
  import { isSafeExternalHTTPURL } from "../../utils/safe-external-url.js";
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
    onreload?: (() => void) | undefined;
    onnewcycle?: (() => void) | undefined;
  }

  let {
    workflow,
    environments,
    initialRef,
    operation,
    state: presentation,
    onsubmit,
    onreload,
    onnewcycle,
  }: Props = $props();
  interface Draft {
    readonly ref: string;
    readonly definitionKey: string;
    readonly values: Readonly<Record<string, unknown>>;
  }
  let drafts = $state<Record<string, Draft>>({});
  let submittedKeys = $state<Record<string, boolean>>({});
  let admissions = $state<Record<string, { blocked: boolean; ownerObserved: boolean }>>({});
  const draftKey = $derived(`${workflow.id}\u0000${workflow.definition_sha}\u0000${initialRef}`);
  const defaultDraft = $derived.by<Draft>(() => {
    return {
      definitionKey: draftKey,
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

  function dropdownOptions(input: NonNullable<Workflow["inputs"]>[number]): SelectDropdownOption[] {
    const values = input.type === "choice"
      ? (input.options ?? [])
      : environments.map((environment) => environment.name);
    const placeholder = input.type === "environment"
      ? "Select an environment"
      : draft.values[input.name] === ""
        ? "Select an option"
        : undefined;
    return [
      ...(placeholder === undefined ? [] : [{ value: "", label: placeholder }]),
      ...values.map((value) => ({ value, label: value })),
    ];
  }

  function dropdownAccessibility(
    controlID: string,
    required: boolean,
    error: string | undefined,
  ): Attachment<HTMLDivElement> {
    return (container) => {
      const control = container.querySelector<HTMLButtonElement>("[role='combobox']");
      if (control === null) return;
      control.id = controlID;
      if (required) control.setAttribute("aria-required", "true");
      else control.removeAttribute("aria-required");
      if (error) {
        control.setAttribute("aria-invalid", "true");
        control.setAttribute("aria-describedby", `${controlID}-error`);
      } else {
        control.removeAttribute("aria-invalid");
        control.removeAttribute("aria-describedby");
      }
    };
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
    <div class="dispatch-outcome">
      <p class="notice notice--error" role="alert">Workflow definition changed. Reload workflows before running it.</p>
      {#if onreload}<Button type="button" tone="workflow" surface="solid" onclick={onreload}>Reload workflows</Button>{/if}
    </div>
  {:else if presentation.kind === "locating"}
    <div class="dispatch-outcome">
      <h2>{workflow.name}</h2>
      <p class="notice" role="status" aria-live="polite">Locating run…</p>
    </div>
  {:else if presentation.kind === "succeeded"}
    <div class="dispatch-outcome">
      <h2>{workflow.name}</h2>
      <p class="notice notice--success" role="status">
        {presentation.message ?? "Workflow accepted."}
      </p>
      {#if presentation.run}
        <dl class="run-details">
          <div><dt>Run ID</dt><dd>{presentation.run.id}</dd></div>
          {#if presentation.run.head_sha}<div><dt>Head SHA</dt><dd><code>{presentation.run.head_sha}</code></dd></div>{/if}
        </dl>
        {#if presentation.run.web_url && isSafeExternalHTTPURL(presentation.run.web_url)}
          <a href={presentation.run.web_url} target="_blank" rel="noopener" aria-label="Open accepted run on provider">
            Open on provider
          </a>
        {/if}
      {/if}
      {#if onnewcycle}<Button type="button" tone="workflow" surface="solid" onclick={onnewcycle}>Run again</Button>{/if}
    </div>
  {:else if presentation.kind === "failed"}
    <div class="dispatch-outcome">
      <h2>{workflow.name}</h2>
      <p class="notice notice--error" role="alert">{presentation.message}</p>
      {#if onnewcycle}<Button type="button" tone="workflow" surface="solid" onclick={onnewcycle}>Run again</Button>{/if}
    </div>
  {:else if presentation.kind === "uncertain"}
    <div class="dispatch-outcome">
      <h2>{workflow.name}</h2>
      <p class="notice notice--error" role="alert">{presentation.message}</p>
      {#if presentation.candidates.length > 0}
        <ul class="candidate-runs" aria-label="Possible workflow runs">
          {#each presentation.candidates as candidate (candidate.id)}
            <li>
              <span>{candidate.id}</span>
              {#if candidate.head_sha}<code>{candidate.head_sha}</code>{/if}
              {#if candidate.web_url && isSafeExternalHTTPURL(candidate.web_url)}
                <a href={candidate.web_url} target="_blank" rel="noopener" aria-label="Open candidate run on provider">
                  Open on provider
                </a>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
      {#if onnewcycle}<Button type="button" tone="workflow" surface="solid" onclick={onnewcycle}>Dispatch again</Button>{/if}
    </div>
  {:else}
    <form class="dispatch-form" novalidate onsubmit={(event) => { event.preventDefault(); submit(); }}>
      <h2>{workflow.name}</h2>
      <label class="field">
        <span>Git ref <span aria-hidden="true">*</span></span>
        <input
          aria-label="Git ref"
          value={draft.ref}
          oninput={(event) => { drafts[draftKey] = { ...draft, ref: event.currentTarget.value }; }}
          disabled={controlsDisabled}
          required
          aria-required="true"
          aria-invalid={errors.ref ? "true" : undefined}
          aria-describedby={errors.ref ? "workflow-ref-error" : undefined}
        />
      </label>
      {#if errors.ref}<p id="workflow-ref-error" class="field-error" role="alert">{errors.ref}</p>{/if}

      {#each workflow.inputs ?? [] as input, index (input.name)}
        <div class="field">
          {#if input.type === "boolean"}
            {@const requiredDescriptionId = `${inputControlId(input.name, index)}-required`}
            <Checkbox
              checked={draft.values[input.name] === true}
              disabled={controlsDisabled}
              {...(input.required ? { ariaDescribedby: requiredDescriptionId } : {})}
              onchange={(checked) => { drafts[draftKey] = { ...draft, values: { ...draft.values, [input.name]: checked } }; }}
            >
              {input.name}{#if input.required} <span aria-hidden="true">*</span>{/if}
            </Checkbox>
            {#if input.required}
              <span id={requiredDescriptionId} class="kit-sr-only">Required input. Checked and unchecked are both valid values.</span>
            {/if}
          {:else}
            <label for={inputControlId(input.name, index)}>{input.name}{#if input.required} <span aria-hidden="true">*</span>{/if}</label>
            {#if input.type === "choice" || input.type === "environment"}
              <div
                class="workflow-input-dropdown"
                {@attach dropdownAccessibility(
                  inputControlId(input.name, index),
                  input.required,
                  errors[input.name],
                )}
              >
                <SelectDropdown
                  title={input.name}
                  value={String(draft.values[input.name] ?? "")}
                  options={dropdownOptions(input)}
                  disabled={controlsDisabled}
                  onchange={(value) => {
                    drafts[draftKey] = {
                      ...draft,
                      values: { ...draft.values, [input.name]: value },
                    };
                  }}
                />
              </div>
            {:else}
              <input
                id={inputControlId(input.name, index)}
                aria-label={input.name}
                type={input.type === "number" ? "number" : "text"}
                disabled={controlsDisabled}
                required={input.required}
                aria-required={input.required ? "true" : undefined}
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
      <Button type="submit" tone="workflow" surface="solid" disabled={controlsDisabled || explicitlyUnavailable || presentation.kind !== "idle"}>{pending || admitted ? "Running workflow…" : "Run workflow"}</Button>
    </form>
  {/if}
</div>

<style>
  .dispatch-root { display: contents; }
  .dispatch-form, .field, .dispatch-outcome { display: grid; gap: var(--space-2); }
  .dispatch-form, .dispatch-outcome { gap: var(--space-4); }
  h2, .notice, .run-details { margin: 0; }
  h2 { font-size: var(--font-size-md); color: var(--text-primary); }
  label, small, dt { font-size: var(--font-size-sm); color: var(--text-secondary); }
  input { box-sizing: border-box; width: 100%; min-height: 32px; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-inset); color: var(--text-primary); padding: 0 var(--space-3); font: inherit; }
  input:focus { outline: 2px solid var(--focus-ring); outline-offset: 1px; }
  .workflow-input-dropdown { min-width: 0; }
  .workflow-input-dropdown :global(.kit-select-dropdown),
  .workflow-input-dropdown :global(.kit-select-dropdown__trigger) { width: 100%; min-width: 0; }
  .field-error, .notice, .run-details, .candidate-runs { font-size: var(--font-size-sm); }
  .field-error, .notice--error { color: var(--status-danger-text, var(--text-danger)); }
  .notice--success { color: var(--status-success-text, var(--text-success)); }
  .run-details, .candidate-runs { display: grid; gap: var(--space-2); }
  .run-details > div { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: var(--space-3); }
  .run-details dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
  .candidate-runs { margin: 0; padding: 0; list-style: none; }
  .candidate-runs li { display: flex; min-width: 0; flex-wrap: wrap; gap: var(--space-2) var(--space-3); }
  a { color: var(--accent-blue); }
</style>
