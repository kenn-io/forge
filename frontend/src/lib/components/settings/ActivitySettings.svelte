<script lang="ts">
  import { Checkbox, SegmentedControl } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import type { ActivitySettings as ActivitySettingsType } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { SettingsWorkflow, settingsErrorMessage } from "../../stores/settings-workflow.js";
  import SettingsOwnerNotice from "./SettingsOwnerNotice.svelte";
  import type { SettingsOwner } from "./settingsOwnership.js";

  const { activity: activityStore } = getStores();
  const runtime = getAppRuntime();

  interface Props {
    activity: ActivitySettingsType;
    onUpdate: (activity: ActivitySettingsType) => void;
    owner?: SettingsOwner;
  }

  let { activity, onUpdate, owner = "local" }: Props = $props();

  let saveVersion = 0;
  let confirmedActivity: ActivitySettingsType | undefined;
  let pendingSaves = 0;

  const TIME_RANGES: { value: ActivitySettingsType["time_range"]; label: string }[] = [
    { value: "24h", label: "24h" },
    { value: "7d", label: "7d" },
    { value: "30d", label: "30d" },
    { value: "90d", label: "90d" },
  ];

  const FILTER_GROUPS = [
    { key: "item_types", label: "Default item types", options: [
      { value: "pr", label: "Pull requests" },
      { value: "issue", label: "Issues" },
    ] },
    { key: "event_types", label: "Default event types", options: [
      { value: "comment", label: "Comments" },
      { value: "review", label: "Reviews" },
      { value: "commit", label: "Commits" },
      { value: "force_push", label: "Force pushes" },
    ] },
  ] as const;

  const FILTER_TOGGLES = [
    { key: "hide_notifications", label: "Hide notifications" },
    { key: "hide_default_branch", label: "Hide default-branch activity" },
    { key: "roll_up_commits", label: "Roll up commits" },
  ] as const;

  function update(changes: Partial<ActivitySettingsType>): void {
    const previous = activity;
    const updated = { ...activity, ...changes };
    onUpdate(updated);
    save(updated, previous);
  }

  function toggleSelection(key: "item_types" | "event_types", value: string): void {
    const selected = activity[key];
    update({ [key]: selected.includes(value) ? selected.filter((item) => item !== value) : [...selected, value] });
  }

  function save(updated: ActivitySettingsType, previous: ActivitySettingsType): void {
    if (pendingSaves === 0) confirmedActivity = previous;
    pendingSaves += 1;
    const version = ++saveVersion;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.persist(() => ({ activity: updated }));
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              if (version === saveVersion) onUpdate(confirmedActivity ?? previous);
              showFlash(settingsErrorMessage(failure), { tone: "danger" });
            }),
          onSuccess: (settings) =>
            Effect.sync(() => {
              confirmedActivity = settings.activity;
              activityStore.hydrateDefaults(settings.activity);
              if (version !== saveVersion) return;
              onUpdate(settings.activity);
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            pendingSaves -= 1;
          }),
        ),
      ),
      {
        operation: "save activity settings",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  function setViewMode(mode: ActivitySettingsType["view_mode"]): void {
    const previous = activity;
    const updated = { ...activity, view_mode: mode };
    onUpdate(updated);
    save(updated, previous);
  }

  function toggleCollapseThreads(): void {
    const previous = activity;
    const updated = { ...activity, collapse_threads: !activity.collapse_threads };
    onUpdate(updated);
    save(updated, previous);
  }

  function setTimeRange(range_: ActivitySettingsType["time_range"]): void {
    const previous = activity;
    const updated = { ...activity, time_range: range_ };
    onUpdate(updated);
    save(updated, previous);
  }

  function toggleHideClosed(): void {
    const previous = activity;
    const updated = { ...activity, hide_closed: !activity.hide_closed };
    onUpdate(updated);
    save(updated, previous);
  }

  function toggleHideBots(): void {
    const previous = activity;
    const updated = { ...activity, hide_bots: !activity.hide_bots };
    onUpdate(updated);
    save(updated, previous);
  }

  function toggleUseWorkspaceActivityForRecency(): void {
    const previous = activity;
    const updated = {
      ...activity,
      use_workspace_activity_for_recency: !activity.use_workspace_activity_for_recency,
    };
    onUpdate(updated);
    save(updated, previous);
  }

  function setViewModeValue(value: string): void {
    if (value === "flat" || value === "threaded") setViewMode(value);
  }

  function setTimeRangeValue(value: string): void {
    if (value === "24h" || value === "7d" || value === "30d" || value === "90d") {
      setTimeRange(value);
    }
  }
</script>

<SettingsOwnerNotice {owner} subject="Activity policy" />

<div class="setting-row">
  <span class="setting-label">Default view mode</span>
  <SegmentedControl
    options={[
      { value: "flat", label: "Flat" },
      { value: "threaded", label: "Threaded" },
    ]}
    value={activity.view_mode}
    onchange={setViewModeValue}
    ariaLabel="Default view mode"
  />
</div>

<div class="setting-row">
  <span class="setting-label">Collapse threads by default</span>
  <button class="toggle-btn" class:toggle-on={activity.collapse_threads} onclick={toggleCollapseThreads} aria-label="Toggle collapse threads by default" aria-pressed={activity.collapse_threads}>
    <span class="toggle-track"><span class="toggle-thumb"></span></span>
  </button>
</div>

<div class="setting-row">
  <span class="setting-label">Default time range</span>
  <SegmentedControl
    options={TIME_RANGES}
    value={activity.time_range}
    onchange={setTimeRangeValue}
    ariaLabel="Default time range"
  />
</div>

{#each FILTER_GROUPS as group (group.key)}
  <fieldset class="filter-group">
    <legend class="setting-label">{group.label}</legend>
    <div class="filter-options">
      {#each group.options as option (option.value)}
        <Checkbox label={option.label} checked={activity[group.key].includes(option.value)} onchange={() => toggleSelection(group.key, option.value)} />
      {/each}
    </div>
  </fieldset>
{/each}

{#each FILTER_TOGGLES as toggle (toggle.key)}
  <div class="setting-row">
    <span class="setting-label">{toggle.label}</span>
    <button class="toggle-btn" class:toggle-on={activity[toggle.key]} onclick={() => update({ [toggle.key]: !activity[toggle.key] })} aria-label={toggle.label} aria-pressed={activity[toggle.key]}>
      <span class="toggle-track"><span class="toggle-thumb"></span></span>
    </button>
  </div>
{/each}

<div class="setting-row">
  <span class="setting-label">Hide closed/merged</span>
  <button class="toggle-btn" class:toggle-on={activity.hide_closed} onclick={toggleHideClosed} aria-label="Toggle hide closed/merged" aria-pressed={activity.hide_closed}>
    <span class="toggle-track"><span class="toggle-thumb"></span></span>
  </button>
</div>

<div class="setting-row">
  <span class="setting-label">Hide bots</span>
  <button class="toggle-btn" class:toggle-on={activity.hide_bots} onclick={toggleHideBots} aria-label="Toggle hide bots" aria-pressed={activity.hide_bots}>
    <span class="toggle-track"><span class="toggle-thumb"></span></span>
  </button>
</div>

<div class="setting-row">
  <span class="setting-label">Use workspace activity for recency</span>
  <button
    class="toggle-btn"
    class:toggle-on={activity.use_workspace_activity_for_recency}
    onclick={toggleUseWorkspaceActivityForRecency}
    aria-label="Toggle use workspace activity for recency"
    aria-pressed={activity.use_workspace_activity_for_recency}
  >
    <span class="toggle-track"><span class="toggle-thumb"></span></span>
  </button>
</div>

<style>
  .filter-group { margin: 0; padding: 0; border: 0; min-width: 0; }
  .filter-options { display: flex; flex-wrap: wrap; gap: var(--space-4); padding-block: var(--space-3); }
  .setting-row { display: flex; align-items: center; justify-content: space-between; min-height: 32px; }
  .setting-label { font-size: var(--font-size-md); color: var(--text-secondary); }
  .toggle-btn { cursor: pointer; padding: 0; background: none; }
  .toggle-track {
    display: block; width: 36px; height: 20px; border-radius: 10px;
    background: var(--bg-inset); border: 1px solid var(--border-muted);
    position: relative; transition: background 0.15s, border-color 0.15s;
  }
  .toggle-on .toggle-track { background: var(--accent-blue); border-color: var(--accent-blue); }
  .toggle-thumb {
    display: block; width: 14px; height: 14px; border-radius: 50%;
    background: white; position: absolute; top: 2px; left: 2px;
    transition: transform 0.15s; box-shadow: var(--shadow-sm);
  }
  .toggle-on .toggle-thumb { transform: translateX(16px); }
</style>
