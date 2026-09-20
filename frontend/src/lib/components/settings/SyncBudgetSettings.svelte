<script lang="ts">
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
  let budgetValid = $state(true);
  let saving = $state(false);

  // The browser's step check is deliberately ignored: step="100" only makes
  // the spinner move in hundreds, while the API accepts any integer in range.
  function validateBudget(input: HTMLInputElement): boolean {
    const budget = Number(input.value);
    budgetValid =
      !input.validity.badInput
      && Number.isInteger(budget)
      && budget >= budgetBounds.minimum
      && budget <= budgetBounds.maximum;
    return budgetValid;
  }

  function onBudgetInput(event: Event): void {
    validateBudget(event.currentTarget as HTMLInputElement);
  }

  function saveBudget(event: Event): void {
    const input = event.currentTarget as HTMLInputElement;
    if (!validateBudget(input)) return;
    const budget = Number(input.value);
    if (budget === sync.budget_per_hour) return;
    saving = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.persist(() => ({ sync: { budget_per_hour: budget } }));
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              input.value = String(sync.budget_per_hour);
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
    <input
      id="sync-budget-per-hour"
      type="number"
      min={budgetBounds.minimum}
      max={budgetBounds.maximum}
      step="100"
      value={sync.budget_per_hour}
      disabled={saving}
      aria-invalid={!budgetValid}
      aria-describedby={budgetValid ? undefined : "sync-budget-per-hour-error"}
      oninput={onBudgetInput}
      onchange={saveBudget}
    />
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
  input { width: 6.5rem; height: 28px; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--bg-primary); color: var(--text-primary); font: inherit; font-size: var(--font-size-sm); padding: 0 8px; }
  input[aria-invalid="true"] { border-color: var(--accent-red); }
</style>
