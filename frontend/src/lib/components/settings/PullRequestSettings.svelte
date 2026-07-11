<script lang="ts">
  import { getStores } from "@middleman/ui";
  import type { PullRequestSettings as PullRequestSettingsType } from "@middleman/ui/api/types";

  import { updateSettings } from "../../api/settings.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";

  interface Props {
    pullRequests: PullRequestSettingsType;
    onUpdate: (settings: PullRequestSettingsType) => void;
  }

  let { pullRequests, onUpdate }: Props = $props();
  const { settings: settingsStore } = getStores();
  const embedded = isEmbedded();
  let saving = $state(false);

  async function toggleMidStackMerges(): Promise<void> {
    if (embedded || saving) return;
    const previous = pullRequests;
    const pending = {
      ...pullRequests,
      allow_mid_stack_merges: !pullRequests.allow_mid_stack_merges,
    };
    onUpdate(pending);
    saving = true;
    try {
      const settings = await updateSettings({ pull_requests: pending });
      onUpdate(settings.pull_requests);
      settingsStore.setPullRequestSettings(settings.pull_requests);
    } catch (err) {
      onUpdate(previous);
      console.warn("Failed to save pull request settings:", err);
    } finally {
      saving = false;
    }
  }
</script>

<div class="setting-row">
  <div class="setting-copy">
    <span class="setting-label">Allow mid-stack merges</span>
    <span class="setting-description">
      When off, only the bottom unmerged branch in a stack can be merged. When on, middleman warns before merging another stack member.
    </span>
  </div>
  <button
    class="toggle-btn"
    class:toggle-on={pullRequests.allow_mid_stack_merges}
    type="button"
    disabled={saving}
    onclick={toggleMidStackMerges}
    aria-label="Allow mid-stack merges"
    aria-pressed={pullRequests.allow_mid_stack_merges}
  >
    <span class="toggle-track"><span class="toggle-thumb"></span></span>
  </button>
</div>

<style>
  .setting-row { display: flex; align-items: center; justify-content: space-between; gap: var(--space-5); min-height: 44px; }
  .setting-copy { display: flex; flex-direction: column; gap: 4px; }
  .setting-label { color: var(--text-secondary); font-size: var(--font-size-md); }
  .setting-description { max-width: 64ch; color: var(--text-muted); font-size: var(--font-size-sm); line-height: 1.4; }
  .toggle-btn { flex: 0 0 auto; cursor: pointer; padding: 0; background: none; }
  .toggle-btn:disabled { cursor: wait; opacity: 0.6; }
  .toggle-track { display: block; width: 36px; height: 20px; border-radius: 10px; background: var(--bg-inset); border: 1px solid var(--border-muted); position: relative; transition: background 0.15s, border-color 0.15s; }
  .toggle-on .toggle-track { background: var(--accent-blue); border-color: var(--accent-blue); }
  .toggle-thumb { display: block; width: 14px; height: 14px; border-radius: 50%; background: white; position: absolute; top: 2px; left: 2px; transition: transform 0.15s; box-shadow: var(--shadow-sm); }
  .toggle-on .toggle-thumb { transform: translateX(16px); }
</style>
