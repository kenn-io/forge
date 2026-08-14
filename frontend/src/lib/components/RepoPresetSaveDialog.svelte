<script lang="ts">
  import {
    SelectDropdown,
    type SelectDropdownOption,
  } from "@kenn-io/kit-ui";
  import type { RepoPreset } from "../api/types.js";
  import DialogButton from "./shared/DialogButton.svelte";
  import Modal from "./shared/Modal.svelte";

  export type RepoPresetSaveTarget =
    | { kind: "overwrite"; name: string }
    | { kind: "create"; name: string };

  interface Props {
    open: boolean;
    presets: RepoPreset[];
    defaultPreset?: RepoPreset | undefined;
    busy?: boolean;
    error?: string | undefined;
    onClose: () => void;
    onSave: (target: RepoPresetSaveTarget) => void;
  }

  let {
    open,
    presets,
    defaultPreset = undefined,
    busy = false,
    error = undefined,
    onClose,
    onSave,
  }: Props = $props();

  let mode = $derived<"overwrite" | "create">(
    defaultPreset ? "overwrite" : "create",
  );
  let overwriteName = $derived(defaultPreset?.name ?? presets[0]?.name ?? "");
  let newName = $state("");
  const overwriteOptions = $derived<SelectDropdownOption[]>(
    presets.map((preset) => ({ value: preset.name, label: preset.name })),
  );

  const normalizedNewName = $derived(newName.trim());
  const duplicateName = $derived(
    presets.some((preset) => preset.name.toLowerCase() === normalizedNewName.toLowerCase()),
  );
  const newNameError = $derived.by(() => {
    if (mode !== "create" || normalizedNewName === "") return undefined;
    if (normalizedNewName.toLowerCase() === "global") return "Global is reserved.";
    if (duplicateName) return "A preset with this name already exists.";
    return undefined;
  });
  const canSave = $derived(
    !busy &&
      (mode === "overwrite"
        ? overwriteName !== ""
        : normalizedNewName !== "" && newNameError === undefined),
  );

  function closeDialog(): void {
    if (!busy) onClose();
  }

  function save(): void {
    if (!canSave) return;
    if (mode === "overwrite") {
      onSave({ kind: "overwrite", name: overwriteName });
      return;
    }
    onSave({ kind: "create", name: normalizedNewName });
  }
</script>

<Modal
  {open}
  title="Save repository preset"
  width={460}
  frameId="save-repository-preset"
  onClose={closeDialog}
>
  <div class="save-preset-content">
    {#if presets.length > 0}
      <label class="save-choice">
        <input
          type="radio"
          name="repo-preset-save-mode"
          value="overwrite"
          checked={mode === "overwrite"}
          disabled={busy}
          onchange={() => (mode = "overwrite")}
        />
        <span>Overwrite preset</span>
      </label>
      <SelectDropdown
        class="preset-select"
        title="Preset to overwrite"
        value={overwriteName}
        options={overwriteOptions}
        onchange={(value) => (overwriteName = value)}
        disabled={busy || mode !== "overwrite"}
      />
    {/if}

    <label class="save-choice">
      <input
        type="radio"
        name="repo-preset-save-mode"
        value="create"
        checked={mode === "create"}
        disabled={busy}
        onchange={() => (mode = "create")}
      />
      <span>Create new preset</span>
    </label>
    <input
      class="preset-name-input"
      type="text"
      aria-label="Preset name"
      placeholder="Preset name"
      bind:value={newName}
      disabled={busy || mode !== "create"}
      onkeydown={(event) => {
        if (event.key === "Enter") save();
      }}
    />

    {#if newNameError}
      <p class="field-error" role="alert">{newNameError}</p>
    {/if}
    {#if error}
      <p class="save-error" role="alert">{error}</p>
    {/if}
  </div>

  {#snippet footer()}
    <DialogButton disabled={busy} onclick={onClose}>Cancel</DialogButton>
    <DialogButton tone="primary" disabled={!canSave} onclick={save}>
      {busy ? "Saving…" : "Save"}
    </DialogButton>
  {/snippet}
</Modal>

<style>
  .save-preset-content {
    display: grid;
    gap: var(--space-3);
  }

  .save-choice {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .preset-name-input {
    width: 100%;
    min-height: 32px;
    box-sizing: border-box;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-primary);
    font: inherit;
    font-size: var(--font-size-sm);
    padding: 0 var(--space-3);
  }

  .preset-name-input:focus {
    border-color: var(--accent-blue);
    outline: none;
  }

  .preset-name-input:disabled {
    opacity: 0.55;
  }

  :global(.preset-select) {
    width: 100%;
  }

  .field-error,
  .save-error {
    margin: 0;
    color: var(--accent-red);
    font-size: var(--font-size-xs);
  }
</style>
