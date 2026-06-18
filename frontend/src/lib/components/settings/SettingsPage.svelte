<script lang="ts">
  import ArrowLeftIcon from "@lucide/svelte/icons/arrow-left";
  import SearchIcon from "@lucide/svelte/icons/search";
  import { onMount } from "svelte";
  import { getStores } from "@middleman/ui";
  import type { Settings } from "@middleman/ui/api/types";
  import { getSettings } from "../../api/settings.js";
  import { navigate } from "../../stores/router.svelte.js";
  import SettingsSection from "./SettingsSection.svelte";
  import RepoSettings from "./RepoSettings.svelte";
  import ActivitySettings from "./ActivitySettings.svelte";
  import TerminalSettings from "./TerminalSettings.svelte";
  import ModeVisibilitySettings from "./ModeVisibilitySettings.svelte";
  import AgentSettings from "./AgentSettings.svelte";
  import FleetSettings from "./FleetSettings.svelte";

  interface SettingsNavItem {
    id: string;
    title: string;
    group: string;
    summary: string;
    keywords: string;
  }

  const { settings: settingsStore } = getStores();

  const navItems: SettingsNavItem[] = [
    {
      id: "settings-repositories",
      title: "Repositories",
      group: "Providers",
      summary: "Tracked repositories and import tools",
      keywords: "repos repositories providers github gitlab forgejo gitea import glob",
    },
    {
      id: "settings-activity",
      title: "Activity",
      group: "Workflow",
      summary: "Default activity feed filters",
      keywords: "activity feed defaults filters time range closed bots",
    },
    {
      id: "settings-terminal",
      title: "Terminal",
      group: "Workspace",
      summary: "Workspace terminal rendering and behavior",
      keywords: "workspace terminal font renderer cursor scrollback ligatures",
    },
    {
      id: "settings-modes",
      title: "Visible modes",
      group: "Navigation",
      summary: "Modes shown in the app header",
      keywords: "visible modes navigation tabs prs issues board reviews docs messages kata",
    },
    {
      id: "settings-agents",
      title: "Workspace agents",
      group: "Workspace",
      summary: "Agent commands available in workspaces",
      keywords: "workspace agents codex claude gemini opencode aider binary arguments",
    },
    {
      id: "settings-fleet",
      title: "Fleet federation",
      group: "Workspace",
      summary: "Remote hosts and fleet membership",
      keywords: "fleet federation remote hosts peers ssh http membership",
    },
  ];

  let settings = $state<Settings | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let searchQuery = $state("");
  let activeSection = $state(navItems[0]!.id);

  const filteredNavItems = $derived.by(() => {
    const query = searchQuery.trim().toLowerCase();
    if (query === "") return navItems;
    return navItems.filter((item) =>
      `${item.title} ${item.group} ${item.summary} ${item.keywords}`
        .toLowerCase()
        .includes(query),
    );
  });

  const groupedNavItems = $derived.by(() => {
    const groups: { group: string; items: SettingsNavItem[] }[] = [];
    for (const item of filteredNavItems) {
      const group = groups.find((candidate) => candidate.group === item.group);
      if (group) {
        group.items.push(item);
      } else {
        groups.push({ group: item.group, items: [item] });
      }
    }
    return groups;
  });

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

  function backToApp(): void {
    if (window.history.length > 1) {
      window.history.back();
      return;
    }
    navigate("/");
  }

  function scrollToSection(id: string): void {
    activeSection = id;
    document.getElementById(id)?.scrollIntoView({
      block: "start",
      behavior: "smooth",
    });
  }

  function handleContentScroll(event: Event): void {
    const scrollPane = event.currentTarget;
    if (!(scrollPane instanceof HTMLElement)) return;
    const paneTop = scrollPane.getBoundingClientRect().top;
    let nextActive = activeSection;
    let closestDistance = Number.POSITIVE_INFINITY;

    for (const item of navItems) {
      const section = document.getElementById(item.id);
      if (!section) continue;
      const distance = Math.abs(section.getBoundingClientRect().top - paneTop - 24);
      if (distance < closestDistance) {
        closestDistance = distance;
        nextActive = item.id;
      }
    }

    activeSection = nextActive;
  }
</script>

<div class="settings-shell">
  <aside class="settings-sidebar" aria-label="Settings navigation">
    <button class="back-button" type="button" onclick={backToApp}>
      <ArrowLeftIcon size="15" strokeWidth="2" aria-hidden="true" />
      <span>Back to app</span>
    </button>

    <label class="settings-search">
      <SearchIcon size="15" strokeWidth="2" aria-hidden="true" />
      <input
        type="search"
        bind:value={searchQuery}
        placeholder="Search settings..."
        aria-label="Search settings"
      />
    </label>

    <nav class="settings-nav" aria-label="Settings sections">
      {#if groupedNavItems.length === 0}
        <p class="empty-nav">No matching settings</p>
      {:else}
        <div class="settings-nav-groups">
          {#each groupedNavItems as group (group.group)}
            <div class="settings-nav-group">
              <div class="settings-nav-group-title">{group.group}</div>
              <div class="settings-nav-items">
                {#each group.items as item (item.id)}
                  <button
                    class={["settings-nav-item", activeSection === item.id && "settings-nav-item--active"]}
                    type="button"
                    onclick={() => scrollToSection(item.id)}
                  >
                    <span>{item.title}</span>
                    <small>{item.summary}</small>
                  </button>
                {/each}
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </nav>
  </aside>

  <div class="settings-scroll-pane" onscroll={handleContentScroll}>
    <div class="settings-page">
      {#if loading}
        <p class="state-msg">Loading settings...</p>
      {:else if error}
        <p class="state-msg state-error">Error: {error}</p>
      {:else if settings}
        <header class="settings-page-header">
          <h1 class="page-title">Settings</h1>
          <p>Configure the local maintainer console without leaving the current workspace.</p>
        </header>

        <SettingsSection title="Repositories" sectionId="settings-repositories">
          <RepoSettings repos={settings.repos} onUpdate={(repos) => { settings = { ...settings!, repos }; settingsStore.setConfiguredRepos(repos); }} />
        </SettingsSection>

        <SettingsSection title="Activity feed defaults" sectionId="settings-activity">
          <ActivitySettings activity={settings.activity} onUpdate={(activity) => { settings = { ...settings!, activity }; }} />
        </SettingsSection>

        <SettingsSection title="Workspace terminal" sectionId="settings-terminal">
          <TerminalSettings
            terminal={settings.terminal}
            onUpdate={(terminal) => {
              settings = { ...settings!, terminal };
              settingsStore.setTerminalSettings(terminal);
            }}
          />
        </SettingsSection>

        <SettingsSection title="Workspace agents" sectionId="settings-agents">
          <AgentSettings
            agents={settings.agents}
            onUpdate={(agents) => {
              settings = { ...settings!, agents };
            }}
          />
        </SettingsSection>

        <SettingsSection title="Fleet federation" sectionId="settings-fleet">
          <FleetSettings
            fleet={settings.fleet}
            onUpdate={(fleet) => {
              settings = { ...settings!, fleet };
            }}
          />
        </SettingsSection>

        <SettingsSection title="Visible modes" sectionId="settings-modes">
          <ModeVisibilitySettings
            modes={settings.modes}
            saveLabel="Save visible modes"
            onUpdate={(modes) => {
              settings = { ...settings!, modes };
              settingsStore.setModeVisibility(modes);
            }}
          />
        </SettingsSection>
      {/if}
    </div>
  </div>
</div>

<style>
  .settings-shell {
    flex: 1 1 auto;
    min-height: 0;
    width: 100%;
    display: flex;
    flex-direction: column;
    background: var(--bg-primary);
  }

  .settings-sidebar {
    flex: 0 0 auto;
    min-width: 0;
    padding: 10px 12px;
    border-bottom: 1px solid var(--border-default);
    background: var(--bg-surface);
  }

  .back-button {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 30px;
    margin-bottom: 10px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .back-button:hover {
    color: var(--text-primary);
  }

  .settings-search {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 34px;
    padding: 0 10px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    color: var(--text-muted);
    background: var(--bg-inset);
  }

  .settings-search input {
    min-width: 0;
    width: 100%;
    border: 0;
    outline: 0;
    color: var(--text-primary);
    background: transparent;
    font-size: var(--font-size-sm);
  }

  .settings-search input::placeholder {
    color: var(--text-muted);
  }

  .settings-nav {
    margin-top: 12px;
  }

  .settings-nav-groups {
    display: flex;
    gap: 10px;
    overflow-x: auto;
    padding-bottom: 2px;
  }

  .settings-nav-group {
    flex: 0 0 min(250px, 76vw);
  }

  .settings-nav-group-title {
    margin: 0 0 6px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 650;
    text-transform: uppercase;
  }

  .settings-nav-items {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .settings-nav-item {
    display: flex;
    flex-direction: column;
    gap: 3px;
    width: 100%;
    min-height: 46px;
    padding: 8px 10px;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    text-align: left;
  }

  .settings-nav-item span {
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 650;
  }

  .settings-nav-item small {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    line-height: 1.25;
  }

  .settings-nav-item:hover {
    background: var(--bg-surface-hover);
  }

  .settings-nav-item--active {
    background: var(--bg-inset);
  }

  .settings-nav-item--active span {
    color: var(--accent-blue);
  }

  .empty-nav {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .settings-scroll-pane {
    flex: 1 1 auto;
    min-height: 0;
    width: 100%;
    overflow-y: auto;
  }

  .settings-page {
    max-width: 760px; margin: 0 auto; padding: 22px 14px 28px;
    display: flex; flex-direction: column; gap: 16px;
  }

  .settings-page-header {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .page-title { font-size: var(--font-size-xl); font-weight: 650; color: var(--text-primary); margin: 0; }

  .settings-page-header p {
    margin: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    line-height: 1.45;
  }

  .state-msg { padding: 40px; text-align: center; color: var(--text-muted); font-size: var(--font-size-md); }
  .state-error { color: var(--accent-red); }

  @media (max-width: 47.999rem) {
    .settings-nav-groups {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      overflow-x: visible;
      gap: 12px;
    }

    .settings-nav-group {
      min-width: 0;
      flex-basis: auto;
    }

    .settings-nav-group-title {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .settings-nav-item {
      min-height: 34px;
      padding: 7px 9px;
    }

    .settings-nav-item small {
      display: none;
    }
  }

  @media (min-width: 48rem) {
    .settings-sidebar {
      padding: 12px 16px;
    }

    .settings-page {
      padding: 26px 20px 34px;
    }
  }

  @media (min-width: 64rem) {
    .settings-shell {
      flex-direction: row;
    }

    .settings-sidebar {
      width: 288px;
      height: 100%;
      padding: 14px 12px;
      overflow-y: auto;
      border-right: 1px solid var(--border-default);
      border-bottom: 0;
    }

    .settings-nav-groups {
      flex-direction: column;
      overflow-x: visible;
      padding-bottom: 0;
    }

    .settings-nav-group {
      flex-basis: auto;
    }

    .settings-page {
      padding: 30px 24px 40px;
    }
  }

  @media (min-width: 80rem) {
    .settings-sidebar {
      width: 320px;
      padding: 16px;
    }

    .settings-page {
      max-width: 820px;
    }
  }
</style>
