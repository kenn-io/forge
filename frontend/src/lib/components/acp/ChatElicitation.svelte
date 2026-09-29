<script lang="ts">
  import { untrack } from "svelte";
  import { Button, Checkbox, SelectDropdown, TextInput, type SelectDropdownOption } from "@kenn-io/kit-ui";
  import type { Elicitation } from "./chat-types.js";
  import {
    canSubmit,
    elicitationContent,
    elicitationFields,
    fieldProblem,
    initialValues,
    type ElicitationChoice,
    type ElicitationField,
    type ElicitationResponse,
    type ElicitationValues,
  } from "./chat-elicitation.js";

  let { elicitation, disabled, onrespond }: {
    elicitation: Elicitation;
    disabled: boolean;
    onrespond: (response: ElicitationResponse) => void;
  } = $props();

  const uid = $props.id();
  const fields = $derived(elicitationFields(elicitation.schema));
  // The parent keys this component by elicitation id, and a pending
  // elicitation's schema never changes, so defaults seed the draft once.
  let values = $state<ElicitationValues>(untrack(() => initialValues(fields)));
  const ready = $derived(canSubmit(fields, values));
  const placeholders: Record<string, string> = { date: "YYYY-MM-DD", "date-time": "YYYY-MM-DDTHH:MM:SSZ" };

  function textOf(key: string): string {
    const value = values[key];
    return typeof value === "string" ? value : "";
  }
  function picksOf(key: string): string[] {
    const value = values[key];
    return Array.isArray(value) ? value : [];
  }
  function toggle(field: ElicitationField & { choices: ElicitationChoice[] }, value: string, checked: boolean) {
    const current = picksOf(field.key);
    values[field.key] = field.choices
      .map((choice) => choice.value)
      .filter((choice) => (choice === value ? checked : current.includes(choice)));
  }
  function selectOptions(field: ElicitationField & { choices: ElicitationChoice[] }): SelectDropdownOption[] {
    const empty = field.required ? { value: "", label: "Choose…", disabled: true } : { value: "", label: "None" };
    return [empty, ...field.choices];
  }
  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (disabled || !ready) return;
    onrespond({ action: "accept", content: elicitationContent(fields, values) });
  }
</script>

{#snippet labelText(field: ElicitationField)}
  {field.label}{#if field.required}<span class="required" aria-hidden="true"> *</span>{/if}
{/snippet}

<form class="elicitation" aria-labelledby={`${uid}-message`} novalidate onsubmit={submit}>
  <p class="message" id={`${uid}-message`}>{elicitation.message || elicitation.schema.title || "The agent needs more information."}</p>
  {#each fields as field, index (field.key)}
    {@const id = `${uid}-${index}`}
    {@const problem = fieldProblem(field, values)}
    {@const describedBy = [field.description ? `${id}-hint` : "", problem ? `${id}-error` : ""].filter(Boolean).join(" ")}
    {@const described = describedBy ? { ariaDescribedby: describedBy } : {}}
    <div class="field">
      {#if field.kind === "boolean"}
        <Checkbox
          checked={values[field.key] === true}
          {disabled}
          {...described}
          onchange={(checked) => { values[field.key] = checked; }}
        >{@render labelText(field)}</Checkbox>
      {:else if field.kind === "multi"}
        <fieldset aria-describedby={describedBy || undefined}>
          <legend>{@render labelText(field)}</legend>
          <div class="choices">
            {#each field.choices as choice (choice.value)}
              <Checkbox
                checked={picksOf(field.key).includes(choice.value)}
                label={choice.label}
                {disabled}
                onchange={(checked) => toggle(field, choice.value, checked)}
              />
            {/each}
          </div>
        </fieldset>
      {:else if field.kind === "select"}
        <span class="label">{@render labelText(field)}</span>
        <div class="select">
          <SelectDropdown
            title={field.required ? `${field.label} (required)` : field.label}
            value={textOf(field.key)}
            options={selectOptions(field)}
            {disabled}
            onchange={(value) => { values[field.key] = value; }}
          />
        </div>
      {:else}
        <label class="label" for={id}>{@render labelText(field)}</label>
        <TextInput
          {id}
          block
          type={field.kind === "text" && field.format === "email" ? "email" : field.kind === "text" && field.format === "uri" ? "url" : "text"}
          value={textOf(field.key)}
          placeholder={field.kind === "text" && field.format ? (placeholders[field.format] ?? "") : ""}
          required={field.required}
          invalid={!!problem}
          {disabled}
          {...described}
          oninput={(value) => { values[field.key] = value; }}
        />
      {/if}
      {#if field.description}<small id={`${id}-hint`}>{field.description}</small>{/if}
      {#if problem}<small class="problem" id={`${id}-error`}>{problem}</small>{/if}
    </div>
  {/each}
  <div class="actions">
    <Button type="submit" size="sm" tone="info" surface="solid" disabled={disabled || !ready}>Submit</Button>
    <Button size="sm" {disabled} onclick={() => onrespond({ action: "decline" })}>Decline</Button>
    <Button size="sm" {disabled} onclick={() => onrespond({ action: "cancel" })}>Cancel</Button>
  </div>
</form>

<style>
  .elicitation { display: flex; flex-direction: column; gap: var(--space-4); min-width: 0; }
  .message { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--text-primary); }
  .field { display: flex; flex-direction: column; gap: var(--space-2); min-width: 0; max-width: 24rem; }
  .label, legend { font-size: var(--font-size-xs); font-weight: var(--font-weight-medium); color: var(--text-secondary); }
  fieldset { margin: 0; padding: 0; border: 0; min-width: 0; }
  legend { padding: 0; margin-bottom: var(--space-2); }
  .choices { display: flex; flex-wrap: wrap; gap: var(--space-2) var(--space-5); }
  .required { color: var(--accent-red); }
  small { font-size: var(--font-size-xs); color: var(--text-secondary); overflow-wrap: anywhere; }
  .problem { color: var(--accent-red); }
  .select :global(.kit-select-dropdown),
  .select :global(.kit-select-dropdown__trigger) { width: 100%; min-width: 0; }
  .actions { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-top: var(--space-1); }
  @media (pointer: coarse) {
    .actions :global(button),
    .select :global(.kit-select-dropdown__trigger),
    .field :global(.kit-text-input),
    .field :global(.kit-checkbox) { min-height: var(--mobile-chrome-hit-target); }
    .actions :global(button) { min-width: var(--mobile-chrome-hit-target); }
    .field :global(.kit-text-input__control) { font-size: var(--font-size-touch-field); }
    .label, legend, small { font-size: var(--font-size-sm); }
  }
</style>
