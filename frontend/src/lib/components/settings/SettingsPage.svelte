<script lang="ts">
  import { onMount } from "svelte";
  import { getStores } from "@middleman/ui";
  import type { Settings } from "@middleman/ui/api/types";
  import { getSettings } from "../../api/settings.js";
  import SettingsSection from "./SettingsSection.svelte";
  import RepoSettings from "./RepoSettings.svelte";
  import ActivitySettings from "./ActivitySettings.svelte";
  import TerminalSettings from "./TerminalSettings.svelte";
  import ModeVisibilitySettings from "./ModeVisibilitySettings.svelte";
  import AgentSettings from "./AgentSettings.svelte";

  const { settings: settingsStore } = getStores();

  let settings = $state<Settings | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  onMount(() => { void loadSettings(); });

  async function loadSettings(): Promise<void> {
    loading = true;
    error = null;
    try {
      settings = await getSettings();
      settingsStore.setConfiguredRepos(settings.repos);
      settingsStore.setModeVisibility(settings.modes);
      settingsStore.setTerminalSettings(settings.terminal);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }
</script>

<div class="settings-page">
  {#if loading}
    <p class="state-msg">Loading settings...</p>
  {:else if error}
    <p class="state-msg state-error">Error: {error}</p>
  {:else if settings}
    <h1 class="page-title">Settings</h1>

    <SettingsSection title="Repositories">
      <RepoSettings repos={settings.repos} onUpdate={(repos) => { settings = { ...settings!, repos }; settingsStore.setConfiguredRepos(repos); }} />
    </SettingsSection>

    <SettingsSection title="Activity feed defaults">
      <ActivitySettings activity={settings.activity} onUpdate={(activity) => { settings = { ...settings!, activity }; }} />
    </SettingsSection>

    <SettingsSection title="Workspace terminal">
      <TerminalSettings
        terminal={settings.terminal}
        onUpdate={(terminal) => {
          settings = { ...settings!, terminal };
          settingsStore.setTerminalSettings(terminal);
        }}
      />
    </SettingsSection>

    <SettingsSection title="Visible modes">
      <ModeVisibilitySettings
        modes={settings.modes}
        saveLabel="Save visible modes"
        onUpdate={(modes) => {
          settings = { ...settings!, modes };
          settingsStore.setModeVisibility(modes);
        }}
      />
    </SettingsSection>

    <SettingsSection title="Workspace agents">
      <AgentSettings
        agents={settings.agents}
        onUpdate={(agents) => {
          settings = { ...settings!, agents };
        }}
      />
    </SettingsSection>
  {/if}
</div>

<style>
  .settings-page {
    max-width: 640px; margin: 0 auto; padding: 24px 16px;
    display: flex; flex-direction: column; gap: 16px;
    overflow-y: auto; height: 100%;
  }
  .page-title { font-size: var(--font-size-xl); font-weight: 600; color: var(--text-primary); margin: 0; }
  .state-msg { padding: 40px; text-align: center; color: var(--text-muted); font-size: var(--font-size-md); }
  .state-error { color: var(--accent-red); }
</style>
