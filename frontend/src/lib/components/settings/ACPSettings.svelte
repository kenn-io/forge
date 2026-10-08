<script lang="ts">
  import { Effect } from "effect";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { schemaConstraints } from "../../api/generated/schema-constraints.js";
  import type { ACPSettings } from "../../api/types.js";
  import { saveACPSettings } from "../../stores/acp-settings-persistence.js";
  import { settingsErrorMessage } from "../../stores/settings-workflow.js";
  import { showFlash } from "../../stores/flash.svelte.js";

  const runtime = getAppRuntime();
  const { settings } = getStores();
  const acp = $derived(settings.getACPSettings());
  let familyEdit = $state.raw<{ value: string } | null>(null);
  let sizeEdit = $state.raw<{ value: number | undefined } | null>(null);
  const fontFamily = $derived(familyEdit?.value ?? acp.font_family);
  const fontSize = $derived(sizeEdit ? sizeEdit.value : acp.font_size);
  let sizeValid = $state(true);
  const bounds = schemaConstraints.ACP.font_size;

  function validateSize(input: HTMLInputElement): boolean {
    const size = Number(input.value);
    sizeValid = input.value !== "" && !input.validity.badInput && Number.isInteger(size)
      && size >= bounds.minimum && size <= bounds.maximum;
    return sizeValid;
  }

  function save(changes: Partial<ACPSettings>): void {
    const submittedFamily = familyEdit;
    const submittedSize = sizeEdit;
    runtime.runCommand(saveACPSettings(settings, changes).pipe(
      Effect.asVoid,
      Effect.ensuring(Effect.sync(() => {
        if (changes.font_family !== undefined && familyEdit === submittedFamily) familyEdit = null;
        if (changes.font_size !== undefined && sizeEdit === submittedSize) sizeEdit = null;
      })),
    ), {
      operation: "save ACP appearance",
      safeContext: {},
      onFailure: (failure) => {
        showFlash(settingsErrorMessage(failure), { tone: "danger" });
      },
    });
  }
</script>

<div class="acp-settings">
  <div class="field">
    <label for="acp-font-family">Font family</label>
    <input id="acp-font-family" type="text"
      bind:value={() => fontFamily, (value) => { familyEdit = { value }; }} placeholder="App default"
      aria-describedby="acp-font-family-help" onchange={() => save({ font_family: fontFamily.trim() })} />
    <small id="acp-font-family-help">Leave blank to use the app default. Applies to all ACP harnesses.</small>
  </div>
  <div class="field size-field">
    <label for="acp-font-size">Font size (px)</label>
    <input id="acp-font-size" type="number" min={bounds.minimum} max={bounds.maximum} step="1"
      bind:value={() => fontSize, (value) => { sizeEdit = { value }; }} aria-invalid={!sizeValid}
      aria-describedby={sizeValid ? undefined : "acp-font-size-error"}
      oninput={(event) => validateSize(event.currentTarget)}
      onchange={(event) => { if (validateSize(event.currentTarget)) save({ font_size: Number(event.currentTarget.value) }); }} />
    {#if !sizeValid}
      <small id="acp-font-size-error" class="error" role="alert">Enter a whole number from {bounds.minimum} to {bounds.maximum}.</small>
    {/if}
  </div>
  <div class="preview" aria-label="ACP font preview" style:font-family={fontFamily || "var(--font-sans)"}
    style:font-size={`${sizeValid ? (fontSize ?? acp.font_size) : acp.font_size}px`}>
    The quick brown fox jumps over the lazy dog. 0123456789
  </div>
</div>

<style>
  .acp-settings { display: flex; flex-direction: column; gap: var(--space-5); }
  .field { display: flex; flex-direction: column; gap: var(--space-2); color: var(--text-secondary); }
  input { width: 100%; min-width: 0; height: 28px; padding: 0 var(--space-4); border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-primary); color: var(--text-primary); font: inherit; font-size: var(--font-size-sm); }
  .size-field input { width: 5.5rem; }
  small { color: var(--text-muted); font-size: var(--font-size-sm); }
  .error { color: var(--accent-red); }
  input[aria-invalid="true"] { border-color: var(--accent-red); }
  .preview { padding: var(--space-4); border: 1px solid var(--border-muted); border-radius: var(--radius-md); color: var(--text-primary); line-height: 1.6; overflow-wrap: anywhere; }
</style>
