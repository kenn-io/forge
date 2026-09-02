<script lang="ts">
  import { Effect } from "effect";
  import { Button, Checkbox } from "@kenn-io/kit-ui";
  import type { DetailSettings as DetailSettingsType } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";
  import SettingsOwnerNotice from "./SettingsOwnerNotice.svelte";
  import type { SettingsOwner } from "./settingsOwnership.js";

  interface Props {
    detail: DetailSettingsType;
    onUpdate: (detail: DetailSettingsType) => void;
    owner?: SettingsOwner;
  }

  let { detail, onUpdate, owner = "local" }: Props = $props();
  const runtime = getAppRuntime();
  const { settings: settingsStore } = getStores();
  const embedded = isEmbedded();
  let saving = $state(false);
  let draft = $derived(String(detail.initial_timeline_entry_limit));
  let collapseDraft = $derived(detail.collapse_single_line_breaks);
  const parsedLimit = $derived(Number(draft));
  const valid = $derived(
    Number.isInteger(parsedLimit) && parsedLimit >= 10 && parsedLimit <= 250,
  );
  const changed = $derived(valid && parsedLimit !== detail.initial_timeline_entry_limit);

  function save(): void {
    if (!changed) return;
    persist({ ...detail, initial_timeline_entry_limit: parsedLimit });
  }

  function toggleCollapseSingleLineBreaks(checked: boolean): void {
    if (checked === detail.collapse_single_line_breaks) return;
    persist({ ...detail, collapse_single_line_breaks: checked });
  }

  function persist(pending: DetailSettingsType): void {
    if (embedded || saving) return;
    const previous = detail;
    saving = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.persist(() => ({ detail: pending }));
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              onUpdate(previous);
              draft = String(previous.initial_timeline_entry_limit);
              collapseDraft = previous.collapse_single_line_breaks;
              showFlash(settingsErrorMessage(failure), { tone: "danger" });
            }),
          onSuccess: (settings) =>
            Effect.sync(() => {
              onUpdate(settings.detail);
              settingsStore.setDetailSettings(settings.detail);
            }),
        }),
        Effect.ensuring(Effect.sync(() => {
          saving = false;
        })),
      ),
      {
        operation: "save detail settings",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }
</script>

<SettingsOwnerNotice {owner} subject="Detail policy" />

<div class="setting-row">
  <div class="setting-copy">
    <label class="setting-label" for="initial-timeline-entry-limit">Initial timeline entries</label>
    <span class="setting-description">
      Additional entries remain available from the detail view.
    </span>
  </div>
  <div class="setting-control">
    <input
      id="initial-timeline-entry-limit"
      type="number"
      min="10"
      max="250"
      step="10"
      bind:value={draft}
      disabled={embedded || saving}
      aria-invalid={!valid}
    />
    <Button
      type="button"
      size="sm"
      disabled={embedded || saving || !changed}
      onclick={save}
      ariaLabel="Save timeline limit"
    >
      {saving ? "Saving..." : "Save"}
    </Button>
  </div>
</div>

<Checkbox
  class="toggle-row"
  bind:checked={collapseDraft}
  disabled={embedded || saving}
  onchange={toggleCollapseSingleLineBreaks}
  ariaLabel="Collapse single line breaks"
>
  <span class="setting-copy">
    <span class="setting-label">Collapse single line breaks</span>
    <span class="setting-description">
      Render descriptions, comments, and commit messages with soft line breaks:
      a single newline joins the paragraph and only a blank line starts a new one.
    </span>
  </span>
</Checkbox>

<style>
  :global(.toggle-row) { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--space-5); }
  :global(.toggle-row .kit-checkbox__box) { order: 2; margin-top: 2px; }
  .setting-row { display: flex; align-items: center; justify-content: space-between; gap: var(--space-5); min-height: 44px; }
  .setting-copy { display: flex; flex-direction: column; gap: 4px; }
  .setting-label { color: var(--text-secondary); font-size: var(--font-size-md); }
  .setting-description { max-width: 64ch; color: var(--text-muted); font-size: var(--font-size-sm); line-height: 1.4; }
  .setting-control { display: flex; align-items: center; gap: var(--space-2); }
  input { width: 5.5rem; height: 28px; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-primary); color: var(--text-primary); font: inherit; font-size: var(--font-size-sm); padding: 0 8px; }
</style>
