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

  // Switched-panel model on kit SettingsLayout: this list is the single
  // source of category order, sidebar labels, and per-panel section header
  // copy. The old scroll-spy page let the nav and section orders drift
  // apart; here they cannot.
  interface SettingsPanelMeta {
    id: string;
    label: string;
    title: string;
    group: string;
    description: string;
    /** Extra search-only terms; never rendered. */
    keywords: string;
  }

  const panels: SettingsPanelMeta[] = [
    {
      id: "settings-repositories",
      label: "Repositories",
      title: "Repositories",
      group: "Providers",
      description: "Tracked repositories and import tools",
      keywords: "repos repositories providers github gitlab forgejo gitea import glob",
    },
    {
      id: "settings-pull-requests",
      label: "Pull requests",
      title: "Pull request safeguards",
      group: "Workflow",
      description: "Merge safeguards for stacked branches",
      keywords: "pull requests merge stack stacked branches safety",
    },
    {
      id: "settings-activity",
      label: "Activity",
      title: "Activity feed defaults",
      group: "Workflow",
      description: "Default activity feed filters",
      keywords: "activity feed defaults filters time range closed bots",
    },
    {
      id: "settings-terminal",
      label: "Terminal",
      title: "Workspace terminal",
      group: "Workspace",
      description: "Workspace terminal rendering and behavior",
      keywords: "workspace terminal font renderer cursor scrollback ligatures",
    },
    {
      id: "settings-kata-projects",
      label: "Kata mappings",
      title: "Kata project mappings",
      group: "Workspace",
      description: "Kata project repository identity overrides",
      keywords: "kata projects repositories mappings workspaces daemon project uid",
    },
    {
      id: "settings-agents",
      label: "Workspace agents",
      title: "Workspace agents",
      group: "Workspace",
      description: "Agent commands available in workspaces",
      keywords: "workspace agents codex claude gemini opencode aider binary arguments",
    },
    {
      id: "settings-fleet",
      label: "Fleet federation",
      title: "Fleet federation",
      group: "Workspace",
      description: "Remote hosts and fleet membership",
      keywords: "fleet federation remote hosts peers ssh http membership",
    },
    {
      id: "settings-modes",
      label: "Visible modes",
      title: "Visible modes",
      group: "Navigation",
      description: "Modes shown in the app header",
      keywords: "visible modes navigation tabs prs issues board reviews docs messages kata",
    },
  ];

  let searchQuery = $state("");

  // The host owns search semantics: kit renders whatever category list it is
  // given, and its display falls back to the first visible category while the
  // bound `active` id is filtered out (the selection itself survives clearing
  // the query). kit renders group headings between runs in array order, so
  // `panels` keeps each group's entries contiguous.
  const categories: SettingsCategory[] = $derived.by(() => {
    const query = searchQuery.trim().toLowerCase();
    const visible =
      query === ""
        ? panels
        : panels.filter((p) =>
            `${p.label} ${p.group} ${p.description} ${p.keywords}`.toLowerCase().includes(query),
          );
    return visible.map((p) => ({ id: p.id, label: p.label, group: p.group, summary: p.description }));
  });

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
