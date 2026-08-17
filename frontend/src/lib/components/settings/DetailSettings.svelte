<script lang="ts">
  import { Effect } from "effect";
  import { Button } from "@kenn-io/kit-ui";
  import type { DetailSettings as DetailSettingsType } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";

  interface Props {
    detail: DetailSettingsType;
    onUpdate: (detail: DetailSettingsType) => void;
  }

  let { detail, onUpdate }: Props = $props();
  const runtime = getAppRuntime();
  const { settings: settingsStore } = getStores();
  const embedded = isEmbedded();
  let saving = $state(false);
  let draft = $derived(String(detail.initial_timeline_entry_limit));
  const parsedLimit = $derived(Number(draft));
  const valid = $derived(
    Number.isInteger(parsedLimit) && parsedLimit >= 10 && parsedLimit <= 250,
  );
  const changed = $derived(valid && parsedLimit !== detail.initial_timeline_entry_limit);

  function save(): void {
    if (embedded || saving || !changed) return;
    const previous = detail;
    const pending = { initial_timeline_entry_limit: parsedLimit };
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

<style>
  .setting-row { display: flex; align-items: center; justify-content: space-between; gap: var(--space-5); min-height: 44px; }
  .setting-copy { display: flex; flex-direction: column; gap: 4px; }
  .setting-label { color: var(--text-secondary); font-size: var(--font-size-md); }
  .setting-description { max-width: 64ch; color: var(--text-muted); font-size: var(--font-size-sm); line-height: 1.4; }
  .setting-control { display: flex; align-items: center; gap: var(--space-2); }
  input { width: 5.5rem; height: 28px; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-primary); color: var(--text-primary); font: inherit; font-size: var(--font-size-sm); padding: 0 8px; }
</style>
