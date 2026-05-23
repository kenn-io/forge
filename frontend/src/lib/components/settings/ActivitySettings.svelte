<script lang="ts">
  import { getStores } from "@middleman/ui";
  import type { ActivitySettings as ActivitySettingsType } from "@middleman/ui/api/types";
  import { updateSettings } from "../../api/settings.js";

  const { activity: activityStore } = getStores();

  interface Props {
    activity: ActivitySettingsType;
    onUpdate: (activity: ActivitySettingsType) => void;
  }

  let { activity, onUpdate }: Props = $props();

  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  const embedded = isEmbedded();

  const TIME_RANGES: { value: ActivitySettingsType["time_range"]; label: string }[] = [
    { value: "24h", label: "24h" },
    { value: "7d", label: "7d" },
    { value: "30d", label: "30d" },
    { value: "90d", label: "90d" },
  ];

  async function save(updated: ActivitySettingsType): Promise<void> {
    if (embedded) return;
    try {
      const settings = await updateSettings({ activity: updated });
      onUpdate(settings.activity);
      activityStore.hydrateDefaults(settings.activity);
    } catch (err) {
      console.warn("Failed to save activity settings:", err);
    }
  }

  function setViewMode(mode: ActivitySettingsType["view_mode"]): void {
    const updated = { ...activity, view_mode: mode };
    onUpdate(updated);
    void save(updated);
  }

  function toggleCollapseThreads(): void {
    const updated = { ...activity, collapse_threads: !activity.collapse_threads };
    onUpdate(updated);
    void save(updated);
  }

  function setTimeRange(range_: ActivitySettingsType["time_range"]): void {
    const updated = { ...activity, time_range: range_ };
    onUpdate(updated);
    void save(updated);
  }

  function toggleHideClosed(): void {
    const updated = { ...activity, hide_closed: !activity.hide_closed };
    onUpdate(updated);
    void save(updated);
  }

  function toggleHideBots(): void {
    const updated = { ...activity, hide_bots: !activity.hide_bots };
    onUpdate(updated);
    void save(updated);
  }
</script>

<div class="setting-row">
  <span class="setting-label">Default view mode</span>
  <div class="segmented-control">
    <button class="seg-btn" class:active={activity.view_mode === "flat"} onclick={() => setViewMode("flat")}>Flat</button>
    <button class="seg-btn" class:active={activity.view_mode === "threaded"} onclick={() => setViewMode("threaded")}>Threaded</button>
  </div>
</div>

<div class="setting-row">
  <span class="setting-label">Collapse threads by default</span>
  <button class="toggle-btn" class:toggle-on={activity.collapse_threads} onclick={toggleCollapseThreads} aria-label="Toggle collapse threads by default" aria-pressed={activity.collapse_threads}>
    <span class="toggle-track"><span class="toggle-thumb"></span></span>
  </button>
</div>

<div class="setting-row">
  <span class="setting-label">Default time range</span>
  <div class="segmented-control">
    {#each TIME_RANGES as r}
      <button class="seg-btn" class:active={activity.time_range === r.value} onclick={() => setTimeRange(r.value)}>{r.label}</button>
    {/each}
  </div>
</div>

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

<style>
  .setting-row { display: flex; align-items: center; justify-content: space-between; min-height: 32px; }
  .setting-label { font-size: var(--font-size-md); color: var(--text-secondary); }
  .segmented-control {
    display: flex; align-items: center; gap: 1px;
    background: var(--bg-inset); border-radius: var(--radius-sm); padding: 2px;
  }
  .seg-btn {
    padding: 4px 12px; font-size: var(--font-size-sm); font-weight: 500; color: var(--text-muted);
    border-radius: calc(var(--radius-sm) - 1px); transition: background 0.12s, color 0.12s;
  }
  .seg-btn.active { background: var(--bg-surface); color: var(--text-primary); box-shadow: var(--shadow-sm); }
  .seg-btn:hover:not(.active) { color: var(--text-secondary); }
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
