<script lang="ts">
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import { onMount } from "svelte";
  import { SearchInput, SettingsLayout, SettingsSection, type SettingsCategory } from "@kenn-io/kit-ui";
  import { getStores } from "@middleman/ui";
  import type { Settings } from "@middleman/ui/api/types";
  import { getSettings } from "../../api/settings.js";
  import { navigate } from "../../stores/router.svelte.js";
  import RepoSettings from "./RepoSettings.svelte";
  import ActivitySettings from "./ActivitySettings.svelte";
  import TerminalSettings from "./TerminalSettings.svelte";
  import ModeVisibilitySettings from "./ModeVisibilitySettings.svelte";
  import AgentSettings from "./AgentSettings.svelte";
  import FleetSettings from "./FleetSettings.svelte";
  import KataProjectMappingsSettings from "./KataProjectMappingsSettings.svelte";
  import PullRequestSettings from "./PullRequestSettings.svelte";
  import { SETTINGS_PANELS, settingsPanelsForModes } from "./settingsPanels.js";

  // Switched-panel model on kit SettingsLayout: this list is the single
  // source of category order, sidebar labels, and per-panel section header
  // copy. The old scroll-spy page let the nav and section orders drift
  // apart; here they cannot.
  let searchQuery = $state("");
  const { settings: settingsStore } = getStores();
  let settings = $state<Settings | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let active = $state(SETTINGS_PANELS[0]!.id);

  // The host owns search semantics: kit renders whatever category list it is
  // given, and its display falls back to the first visible category while the
  // bound `active` id is filtered out (the selection itself survives clearing
  // the query). kit renders group headings between runs in array order, so
  // `visiblePanels` keeps each group's entries contiguous.
  const visiblePanels = $derived.by(() => {
    const loaded = settings;
    return loaded ? settingsPanelsForModes(loaded.modes?.kata === true) : SETTINGS_PANELS;
  });
  const categories: SettingsCategory[] = $derived.by(() => {
    const query = searchQuery.trim().toLowerCase();
    const visible =
      query === ""
        ? visiblePanels
        : visiblePanels.filter((p) =>
            `${p.label} ${p.group} ${p.description} ${p.keywords}`.toLowerCase().includes(query),
          );
    return visible.map((p) => ({ id: p.id, label: p.label, group: p.group, summary: p.description }));
  });

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
      settingsStore.setPullRequestSettings(settings.pull_requests);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  function backToApp(): void {
    // Always route to an in-app destination rather than window.history.back():
    // on a direct or bookmarked /settings visit the previous history entry can
    // be an unrelated site, and history.back() would navigate the user out of
    // middleman entirely. The header's settings toggle owns exact-route return;
    // this in-page control only needs a guaranteed in-app landing.
    navigate("/");
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
      {#snippet sidebarHeader()}
        <button class="back-button" type="button" onclick={backToApp}>
          <ArrowLeftIcon size="15" strokeWidth="2" aria-hidden="true" />
          <span>Back to app</span>
        </button>
        <SearchInput
          bind:value={searchQuery}
          placeholder="Search settings..."
          ariaLabel="Search settings"
          size="sm"
          block
        />
        {#if categories.length === 0}
          <p class="empty-nav">No matching settings</p>
        {/if}
      {/snippet}
      {#snippet panel(activeId)}
        <!-- Every panel stays mounted; only the active one is shown. Panel
             components keep unsaved edits in local draft state, so switching
             categories must hide, not unmount, or drafts are silently lost. -->
        {#each SETTINGS_PANELS as meta (meta.id)}
          {@const panelVisible = visiblePanels.some((panel) => panel.id === meta.id)}
          <div class="settings-panel" hidden={!panelVisible || meta.id !== activeId}>
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
          {:else if meta.id === "settings-pull-requests"}
            <PullRequestSettings
              pullRequests={loaded.pull_requests}
              onUpdate={(pull_requests) => {
                settings = { ...settings!, pull_requests };
                settingsStore.setPullRequestSettings(pull_requests);
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
              enabled={loaded.modes?.kata === true}
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

  .back-button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 30px;
    margin-bottom: 8px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .back-button:hover {
    color: var(--text-primary);
  }

  .empty-nav {
    margin: 8px 0 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
</style>
