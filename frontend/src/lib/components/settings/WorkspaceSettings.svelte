<script lang="ts">
  import type { Settings } from "../../api/types.js";

  import { updateSettings } from "../../api/settings.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";

  interface Props {
    workspaces: Settings["workspaces"];
    onUpdate: (settings: Settings["workspaces"]) => void;
  }

  let { workspaces, onUpdate }: Props = $props();
  const embedded = isEmbedded();
  let saving = $state(false);

  async function toggleAutoAssign(): Promise<void> {
    if (embedded || saving) return;
    const previous = workspaces;
    const pending = {
      ...workspaces,
      auto_assign_on_create: !workspaces.auto_assign_on_create,
    };
    onUpdate(pending);
    saving = true;
    try {
      const settings = await updateSettings({ workspaces: pending });
      onUpdate(settings.workspaces);
    } catch (err) {
      onUpdate(previous);
      console.warn("Failed to save workspace settings:", err);
    } finally {
      saving = false;
    }
  }
</script>

<div class="settings-list">
  <div class="setting-row">
    <div class="setting-copy">
      <span class="setting-label">Assign new workspace items to me</span>
      <span class="setting-description">
        When creating a workspace from a pull request or issue, add your provider account as an assignee so teammates can see that you are working on it.
      </span>
    </div>
    <button
      class={["toggle-btn", workspaces.auto_assign_on_create && "toggle-on"]}
      type="button"
      disabled={saving}
      onclick={toggleAutoAssign}
      aria-label="Assign new workspace items to me"
      aria-pressed={workspaces.auto_assign_on_create}
    >
      <span class="toggle-track"><span class="toggle-thumb"></span></span>
    </button>
  </div>
</div>

<style>
  .settings-list { display: flex; flex-direction: column; gap: var(--space-4); }
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
