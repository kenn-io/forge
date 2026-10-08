<script lang="ts">
  import { tick, untrack } from "svelte";
  import { Button, Checkbox, TextInput } from "@kenn-io/kit-ui";
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
  // After a failed submit every problem shows, including missing answers.
  let attempted = $state(false);
  // One answer per request: the form locks once sent until the agent's next
  // state drops it. A change in `disabled` (a dropped connection) unlocks it
  // so an answer that never arrived can be resent.
  let sent = $derived.by(() => {
    void disabled;
    return false;
  });
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
  function respond(response: ElicitationResponse) {
    if (disabled || sent) return;
    sent = true;
    onrespond(response);
  }
  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (disabled || sent) return;
    const form = event.currentTarget as HTMLFormElement;
    if (!canSubmit(fields, values)) {
      attempted = true;
      await tick();
      form.querySelector<HTMLElement>('[aria-invalid="true"] input, input[aria-invalid="true"]')?.focus();
      return;
    }
    respond({ action: "accept", content: elicitationContent(fields, values) });
  }
</script>

{#snippet labelText(field: ElicitationField)}
  {field.label}{#if field.required}<span class="required" aria-hidden="true"> *</span>{/if}
{/snippet}

{#snippet choiceText(choice: ElicitationChoice)}
  <span class="choice-text">
    <span class="choice-title">{choice.label}</span>
    {#if choice.description}<span class="choice-description">{choice.description}</span>{/if}
  </span>
{/snippet}

<form class="elicitation" aria-labelledby={`${uid}-message`} novalidate onsubmit={submit}>
  <p class="message" id={`${uid}-message`}>{elicitation.message || elicitation.schema.title || "The agent needs more information."}</p>
  {#if elicitation.message && elicitation.schema.title}<p class="schema-title">{elicitation.schema.title}</p>{/if}
  {#if elicitation.schema.description}<p class="schema-description">{elicitation.schema.description}</p>{/if}
  {#each fields as field, index (field.key)}
    {@const id = `${uid}-${index}`}
    {@const problem = fieldProblem(field, values, attempted)}
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
      {:else if field.kind === "multi" || field.kind === "select"}
        {@const picked = field.kind === "multi" ? picksOf(field.key) : [textOf(field.key)]}
        {@const preview = field.kind === "select" ? field.choices.find((choice) => choice.value === picked[0])?.preview : ""}
        <fieldset
          aria-describedby={describedBy || undefined}
          role={field.kind === "select" ? "radiogroup" : undefined}
          aria-required={field.kind === "select" && field.required ? "true" : undefined}
          aria-invalid={problem ? "true" : undefined}
        >
          <legend>{@render labelText(field)}</legend>
          <div class="choices">
            {#each field.choices as choice (choice.value)}
              {@const checked = picked.includes(choice.value)}
              {@const row = ["choice", checked && "choice--selected", disabled && "choice--disabled"].filter(Boolean).join(" ")}
              {#if field.kind === "multi"}
                <Checkbox class={row} {checked} {disabled} onchange={(next) => toggle(field, choice.value, next)}>
                  {@render choiceText(choice)}
                </Checkbox>
              {:else}
                <label class={row}>
                  <input
                    type="radio"
                    name={id}
                    value={choice.value}
                    {checked}
                    {disabled}
                    onchange={() => { values[field.key] = choice.value; }}
                  />
                  {@render choiceText(choice)}
                </label>
              {/if}
            {/each}
          </div>
          {#if preview}<pre class="preview" aria-label={`${field.label} preview`}>{preview}</pre>{/if}
          {#if field.kind === "select" && !field.required && picked[0]}
            <div class="clear">
              <Button size="sm" {disabled} onclick={() => { values[field.key] = ""; }}>Clear selection</Button>
            </div>
          {/if}
        </fieldset>
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
    <Button type="submit" size="sm" tone="info" surface="solid" disabled={disabled || sent}>Submit</Button>
    <Button size="sm" disabled={disabled || sent} onclick={() => respond({ action: "decline" })}>Decline</Button>
    <Button size="sm" disabled={disabled || sent} onclick={() => respond({ action: "cancel" })}>Cancel</Button>
  </div>
</form>

<style>
  .elicitation { display: flex; flex-direction: column; gap: var(--space-4); min-width: 0; }
  .message { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--text-primary); }
  .schema-title { margin: 0; font-weight: var(--font-weight-medium); color: var(--text-primary); overflow-wrap: anywhere; }
  .schema-description { margin: 0; font-size: var(--font-size-sm); color: var(--text-secondary); white-space: pre-wrap; overflow-wrap: anywhere; }
  .field { display: flex; flex-direction: column; gap: var(--space-2); min-width: 0; }
  .label, legend { font-size: var(--font-size-xs); font-weight: var(--font-weight-medium); color: var(--text-secondary); }
  fieldset { margin: 0; padding: 0; border: 0; min-width: 0; }
  legend { padding: 0; margin-bottom: var(--space-2); }
  .choices { display: flex; flex-direction: column; border: 1px solid var(--border-muted); border-radius: var(--radius-md); overflow: hidden; }
  fieldset[aria-invalid="true"] .choices { border-color: var(--accent-red); }
  .choices :global(.choice) { display: flex; align-items: flex-start; gap: var(--space-3); padding: var(--space-2) var(--space-3); border-bottom: 1px solid var(--border-muted); cursor: pointer; }
  .choices :global(.choice:last-child) { border-bottom: 0; }
  .choices :global(.choice:hover) { background: var(--bg-surface-hover); }
  .choices :global(.choice--selected) { background: color-mix(in srgb, var(--accent-blue) 8%, transparent); }
  .choices :global(.choice--disabled) { cursor: not-allowed; opacity: 0.62; }
  .choices :global(.kit-checkbox__box) { margin-top: 2px; }
  .choice input { margin: 3px 0 0; flex-shrink: 0; accent-color: var(--accent-blue); }
  .choice-text { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .choice-title { font-size: var(--font-size-sm); font-weight: var(--font-weight-medium); color: var(--text-primary); overflow-wrap: anywhere; }
  .choice-description { font-size: var(--font-size-xs); color: var(--text-secondary); overflow-wrap: anywhere; white-space: pre-wrap; }
  .preview { margin: var(--space-2) 0 0; padding: var(--space-2) var(--space-3); max-height: 16rem; overflow: auto; border: 1px solid var(--border-muted); border-radius: var(--radius-md); background: var(--bg-inset); font-family: var(--font-mono); font-size: var(--font-size-xs); color: var(--text-primary); white-space: pre; }
  .clear { margin-top: var(--space-2); }
  .required { color: var(--accent-red); }
  small { font-size: var(--font-size-xs); color: var(--text-secondary); overflow-wrap: anywhere; }
  .problem { color: var(--accent-red); }
  .actions { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-top: var(--space-1); }
  @media (pointer: coarse) {
    .actions :global(button),
    .choices :global(.choice),
    .field :global(.kit-text-input),
    .field :global(.kit-checkbox) { min-height: var(--mobile-chrome-hit-target); }
    .actions :global(button) { min-width: var(--mobile-chrome-hit-target); }
    .field :global(.kit-text-input__control) { font-size: var(--font-size-touch-field); }
    .label, legend, small { font-size: var(--font-size-sm); }
  }
</style>
