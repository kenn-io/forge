<script lang="ts">
  import { NumberInput } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import type { SyncSettings as SyncSettingsType } from "../../api/types.js";
  import { schemaConstraints } from "../../api/generated/schema-constraints.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";
  import SettingsOwnerNotice from "./SettingsOwnerNotice.svelte";
  import type { SettingsOwner } from "./settingsOwnership.js";

  interface Props {
    sync: SyncSettingsType;
    onUpdate: (sync: SyncSettingsType) => void;
    owner?: SettingsOwner;
  }

  let { sync, onUpdate, owner = "local" }: Props = $props();
  const runtime = getAppRuntime();
  const { sync: syncStore } = getStores();
  // The server's bounds come from the OpenAPI schema, so the input can reject
  // an out-of-range budget before any request is sent.
  const budgetBounds = schemaConstraints.SyncSettingsUpdate.budget_per_hour;
  let saving = $state(false);
  // NumberInput clamps to min and max when the field is left. An out-of-range
  // budget must stay on screen and must not be saved as the nearest bound.
  // The draft applies only while the saved budget is unchanged, so a later
  // settings update replaces it.
  let budgetDraft = $state<{ from: number; value: number | null } | null>(null);
  const budgetValue = $derived(
    budgetDraft !== null && budgetDraft.from === sync.budget_per_hour
      ? budgetDraft.value
      : sync.budget_per_hour,
  );

  function wholeBudget(value: number | null): value is number {
    return value !== null
      && Number.isInteger(value)
      && value >= budgetBounds.minimum
      && value <= budgetBounds.maximum;
  }

  const budgetValid = $derived(wholeBudget(budgetValue));

  function onBudgetInput(value: number | null): void {
    budgetDraft = { from: sync.budget_per_hour, value };
  }

  function saveBudget(value: number | null): void {
    if (!wholeBudget(value)) return;
    if (value === sync.budget_per_hour) return;
    const budget = value;
    saving = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.persist(() => ({ sync: { budget_per_hour: budget } }));
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              budgetDraft = null;
              showFlash(settingsErrorMessage(failure), { tone: "danger" });
            }),
          onSuccess: (settings) =>
            Effect.sync(() => {
              onUpdate(settings.sync);
              // The status bar reads the live ceiling; show the new limit now
              // instead of on the next poll.
              syncStore.refreshRateLimits();
            }),
        }),
        Effect.ensuring(Effect.sync(() => (saving = false))),
      ),
      {
        operation: "save sync budget",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }
</script>

<SettingsOwnerNotice {owner} subject="The sync budget" />

<div class="setting-row">
  <div class="setting-copy">
    <label class="setting-label" for="sync-budget-per-hour">Hourly sync budget</label>
    <span class="setting-description">
      API requests background sync may spend each hour, per provider account. Applies
      immediately. Raising it spends more of your provider quota; it does not increase that quota.
    </span>
  </div>
  <div class="setting-control">
    <div class="number-field">
      <NumberInput
        id="sync-budget-per-hour"
        block
        step={100}
        value={budgetValue}
        disabled={saving}
        invalid={!budgetValid}
        {...(budgetValid ? {} : { ariaDescribedby: "sync-budget-per-hour-error" })}
        oninput={onBudgetInput}
        onchange={saveBudget}
      />
    </div>
    {#if !budgetValid}
      <span id="sync-budget-per-hour-error" class="setting-error" role="alert">
        Enter a whole number from {budgetBounds.minimum} to {budgetBounds.maximum}.
      </span>
    {/if}
  </div>
</div>

<style>
  .setting-row { display: flex; align-items: center; justify-content: space-between; gap: var(--space-5); min-height: 44px; margin-top: var(--space-4); }
  .setting-copy { display: flex; flex-direction: column; gap: 4px; }
  .setting-label { color: var(--text-secondary); font-size: var(--font-size-md); }
  .setting-description { max-width: 64ch; color: var(--text-muted); font-size: var(--font-size-sm); line-height: 1.4; }
  .setting-control { display: flex; flex-direction: column; align-items: flex-end; gap: 4px; }
  .setting-error { color: var(--accent-red); font-size: var(--font-size-sm); }
  .number-field { width: 6.5rem; }
</style>
