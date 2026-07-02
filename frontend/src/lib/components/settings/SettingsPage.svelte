<script lang="ts">
  import { onMount } from "svelte";
  import { SettingsLayout, SettingsSection, type SettingsCategory } from "@kenn-io/kit-ui";
  import { getStores } from "@middleman/ui";
  import type { Settings } from "@middleman/ui/api/types";
  import { getSettings } from "../../api/settings.js";
  import RepoSettings from "./RepoSettings.svelte";
  import ActivitySettings from "./ActivitySettings.svelte";
  import TerminalSettings from "./TerminalSettings.svelte";
  import ModeVisibilitySettings from "./ModeVisibilitySettings.svelte";
  import AgentSettings from "./AgentSettings.svelte";
  import FleetSettings from "./FleetSettings.svelte";
  import KataProjectMappingsSettings from "./KataProjectMappingsSettings.svelte";

  // Switched-panel model on kit SettingsLayout: this list is the single
  // source of category order, sidebar labels, and per-panel section header
  // copy. The old scroll-spy page let the nav and section orders drift
  // apart; here they cannot.
  interface SettingsPanelMeta {
    id: string;
    label: string;
    title: string;
    description: string;
  }

  const panels: SettingsPanelMeta[] = [
    {
      id: "settings-repositories",
      label: "Repositories",
      title: "Repositories",
      description: "Tracked repositories and import tools",
    },
    {
      id: "settings-activity",
      label: "Activity",
      title: "Activity feed defaults",
      description: "Default activity feed filters",
    },
    {
      id: "settings-terminal",
      label: "Terminal",
      title: "Workspace terminal",
      description: "Workspace terminal rendering and behavior",
    },
    {
      id: "settings-kata-projects",
      label: "Kata mappings",
      title: "Kata project mappings",
      description: "Kata project repository identity overrides",
    },
    {
      id: "settings-modes",
      label: "Visible modes",
      title: "Visible modes",
      description: "Modes shown in the app header",
    },
    {
      id: "settings-agents",
      label: "Workspace agents",
      title: "Workspace agents",
      description: "Agent commands available in workspaces",
    },
    {
      id: "settings-fleet",
      label: "Fleet federation",
      title: "Fleet federation",
      description: "Remote hosts and fleet membership",
    },
  ];

  const categories: SettingsCategory[] = panels.map((p) => ({ id: p.id, label: p.label }));

  const { settings: settingsStore } = getStores();

  let settings = $state<Settings | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let active = $state(panels[0]!.id);

  onMount(() => {
    void loadSettings();
  });

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

<!-- The settings-page class stays on the route container: route-level specs
     (e2e navigation) key on it to know the settings route rendered. -->
<div class="settings-page">
  {#if loading}
    <p class="state-msg">Loading settings...</p>
  {:else if error}
    <p class="state-msg state-error">Error: {error}</p>
  {:else if settings}
    {@const loaded = settings}
    <SettingsLayout {categories} bind:active title="Settings">
      {#snippet panel(activeId)}
        <!-- Every panel stays mounted; only the active one is shown. Panel
             components keep unsaved edits in local draft state, so switching
             categories must hide, not unmount, or drafts are silently lost. -->
        {#each panels as meta (meta.id)}
          <div class="settings-panel" hidden={meta.id !== activeId}>
            <SettingsSection title={meta.title} description={meta.description}>
              {#if meta.id === "settings-repositories"}
            <RepoSettings
              repos={loaded.repos}
              onUpdate={(repos) => {
                settings = { ...settings!, repos };
                settingsStore.setConfiguredRepos(repos);
              }}
            />
          {:else if meta.id === "settings-activity"}
            <ActivitySettings
              activity={loaded.activity}
              onUpdate={(activity) => {
                settings = { ...settings!, activity };
              }}
            />
          {:else if meta.id === "settings-terminal"}
            <TerminalSettings
              terminal={loaded.terminal}
              onUpdate={(terminal) => {
                settings = { ...settings!, terminal };
                settingsStore.setTerminalSettings(terminal);
              }}
            />
          {:else if meta.id === "settings-kata-projects"}
            <KataProjectMappingsSettings
              mappings={loaded.kata_projects}
              repos={loaded.repos}
              onUpdate={(kata_projects) => {
                settings = { ...settings!, kata_projects };
              }}
            />
          {:else if meta.id === "settings-modes"}
            <ModeVisibilitySettings
              modes={loaded.modes}
              saveLabel="Save visible modes"
              onUpdate={(modes) => {
                settings = { ...settings!, modes };
                settingsStore.setModeVisibility(modes);
              }}
            />
          {:else if meta.id === "settings-agents"}
            <AgentSettings
              agents={loaded.agents}
              onUpdate={(agents) => {
                settings = { ...settings!, agents };
              }}
            />
          {:else if meta.id === "settings-fleet"}
            <FleetSettings
              fleet={loaded.fleet}
              onUpdate={(fleet) => {
                settings = { ...settings!, fleet };
              }}
            />
              {/if}
            </SettingsSection>
          </div>
        {/each}
      {/snippet}
    </SettingsLayout>
  {/if}
</div>

<style>
  .settings-page {
    display: flex;
    flex: 1 1 auto;
    min-height: 0;
    width: 100%;
  }

  .state-msg {
    padding: 24px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .state-error {
    color: var(--accent-red);
  }

  .settings-panel[hidden] {
    display: none;
  }
</style>
