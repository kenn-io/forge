<script lang="ts">
  import { Checkbox } from "@kenn-io/kit-ui";
  import {
    DEFAULT_MODE_VISIBILITY,
    getStores,
  } from "@middleman/ui";
  import { showFlash } from "@middleman/ui/stores/flash";
  import type { ModeVisibility } from "@middleman/ui/api/types";
  import { updateSettings } from "../../api/settings.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";

  type ModeKey = keyof ModeVisibility;

  interface ModeOption {
    key: ModeKey;
    label: string;
  }

  interface Props {
    modes: ModeVisibility | null | undefined;
    onUpdate: (modes: ModeVisibility) => void;
    compact?: boolean;
    saveLabel?: string;
    onSavingChange?: (saving: boolean) => void;
  }

  let {
    modes,
    onUpdate,
    compact = false,
    saveLabel = "Save",
    onSavingChange,
  }: Props = $props();

  const { settings: settingsStore } = getStores();
  const embedded = isEmbedded();

  const modeOptions: ModeOption[] = [
    { key: "activity", label: "Activity" },
    { key: "repos", label: "Repos" },
    { key: "kata", label: "Kata" },
    { key: "docs", label: "Docs" },
    { key: "messages", label: "Messages" },
    { key: "pulls", label: "PRs" },
    { key: "issues", label: "Issues" },
    { key: "board", label: "Board" },
    { key: "reviews", label: "Reviews" },
    { key: "workspaces", label: "Workspaces" },
  ];

  let draft = $state<ModeVisibility>({ ...DEFAULT_MODE_VISIBILITY });
  let source = $state<ModeVisibility>({ ...DEFAULT_MODE_VISIBILITY });
  let saving = $state(false);

  function normalizeModes(value: ModeVisibility | null | undefined): ModeVisibility {
    return {
      ...DEFAULT_MODE_VISIBILITY,
      ...(value ?? {}),
    };
  }

  function sameModes(left: ModeVisibility, right: ModeVisibility): boolean {
    return modeOptions.every((option) => left[option.key] === right[option.key]);
  }

  $effect(() => {
    const next = normalizeModes(modes);
    if (sameModes(next, source)) return;
    source = next;
    draft = next;
  });

  const canSave = $derived(!saving && !sameModes(draft, source));

  function toggleMode(key: ModeKey): void {
    draft = {
      ...draft,
      [key]: !draft[key],
    };
  }

  async function save(): Promise<void> {
    if (embedded) return;
    if (!canSave) return;

    saving = true;
    onSavingChange?.(true);
    const pendingModes = normalizeModes(draft);
    try {
      const settings = await updateSettings({ modes: pendingModes });
      const updated = normalizeModes(settings.modes ?? pendingModes);
      source = updated;
      draft = updated;
      onUpdate(updated);
      settingsStore.setModeVisibility(updated);
    } catch (err) {
      draft = source;
      showFlash(err instanceof Error ? err.message : "Failed to save visible modes.", { tone: "danger" });
    } finally {
      saving = false;
      onSavingChange?.(false);
    }
  }
</script>

<div class={["mode-visibility-settings", compact && "compact"].filter(Boolean).join(" ")}>
  <div class="mode-grid">
    {#each modeOptions as option (option.key)}
      <Checkbox
        class="mode-toggle"
        checked={draft[option.key]}
        disabled={saving}
        label={option.label}
        onchange={(checked) => {
          if (checked !== draft[option.key]) toggleMode(option.key);
        }}
      />
    {/each}
  </div>

  <div class="actions">
    <button
      class="save-btn"
      type="button"
      disabled={!canSave}
      onclick={save}
    >
      {saving ? "Saving..." : saveLabel}
    </button>
  </div>
</div>

<style>
  .mode-visibility-settings {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .mode-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--space-4) var(--space-5);
  }

  .mode-visibility-settings :global(.mode-toggle) {
    min-width: 0;
    line-height: 1.2;
  }

  .mode-visibility-settings :global(.mode-toggle .kit-checkbox__label) {
    overflow-wrap: anywhere;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
  }

  .save-btn {
    min-height: 28px;
    padding: 5px 12px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .save-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .save-btn:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .compact {
    gap: var(--space-4);
  }

  .compact .mode-grid {
    gap: var(--space-3) var(--space-5);
  }
</style>
