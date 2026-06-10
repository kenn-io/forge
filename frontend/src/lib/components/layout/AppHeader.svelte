<script lang="ts">
  import { getStores, KbdBadge, SelectDropdown } from "@middleman/ui";
  import type { ModeVisibility } from "@middleman/ui/api/types";
  import { SvelteMap } from "svelte/reactivity";
  import {
    getBasePath,
    getPage,
    getView,
    navigate,
  } from "../../stores/router.svelte.ts";
  import {
    activitySelectionToRoute,
    parseActivitySelection,
  } from "../../utils/activitySelection.js";
  import RepoTypeahead from "../RepoTypeahead.svelte";
  import HeaderIconButton from "./HeaderIconButton.svelte";
  import {
    MoonIcon,
    SearchIcon,
    SettingsIcon,
    SidebarToggleIcon,
    SpinnerIcon,
    SunIcon,
    SyncIcon,
  } from "../../icons.ts";
  import { getGlobalRepo, setGlobalRepo } from "../../stores/filter.svelte.js";
  import { isEmbedded, getUIConfig } from "../../stores/embed-config.svelte.js";
  import {
    getContainerSize,
    isNarrow,
  } from "../../stores/container.svelte.js";
  import {
    isDark, toggleTheme, isThemeToggleVisible,
  } from "../../stores/theme.svelte.js";
  import {
    isSidebarCollapsed,
    toggleSidebar,
    isSidebarToggleEnabled,
  } from "../../stores/sidebar.svelte.js";
  import { openPalette } from "../../stores/keyboard/palette-state.svelte.js";

  const appIconSrc = `${getBasePath().replace(/\/$/, "")}/favicon.svg`;

  const hasSidebarStrip = $derived(
    getPage() === "issues"
    || (getPage() === "pulls" && getView() === "list")
    || getPage() === "workspaces"
    || getPage() === "terminal",
  );

  const stores = getStores();
  const { settings, sync } = stores;

  type ModeKey = keyof ModeVisibility;
  type NavDestination =
    | "activity"
    | "repos"
    | "kata"
    | "docs"
    | "messages"
    | "pulls"
    | "issues"
    | "board"
    | "reviews"
    | "workspaces";
  type CompactNavValue = NavDestination | "settings" | "design-system";

  const modeNavOptions: { value: NavDestination; label: string; mode: ModeKey }[] = [
    { value: "activity", label: "Activity", mode: "activity" },
    { value: "repos", label: "Repos", mode: "repos" },
    { value: "kata", label: "Kata", mode: "kata" },
    { value: "docs", label: "Docs", mode: "docs" },
    { value: "messages", label: "Messages", mode: "messages" },
    { value: "pulls", label: "PRs", mode: "pulls" },
    { value: "issues", label: "Issues", mode: "issues" },
    { value: "board", label: "Board", mode: "board" },
    { value: "reviews", label: "Reviews", mode: "reviews" },
    { value: "workspaces", label: "Workspaces", mode: "workspaces" },
  ];

  async function handleSync(): Promise<void> {
    if (sync.getSyncState()?.running) return;
    await sync.triggerSync();
  }

  const syncing = $derived(sync.getSyncState()?.running ?? false);
  const compactHeader = $derived(getContainerSize() !== "wide");
  const showProviderRepoSelector = $derived(
    !getUIConfig().hideRepoSelector &&
      (getPage() === "activity" ||
        getPage() === "repos" ||
        getPage() === "pulls" ||
        getPage() === "issues"),
  );
  let settingsReturnPath = "/";

  function currentAppPath(): string {
    const base = getBasePath();
    const basePrefix = base === "/" ? "" : base.replace(/\/$/, "");
    const fullPath = window.location.pathname + window.location.search;
    if (basePrefix && fullPath.startsWith(basePrefix)) {
      return fullPath.slice(basePrefix.length) || "/";
    }
    return fullPath;
  }

  function toggleSettings(): void {
    if (getPage() === "settings") {
      navigate(settingsReturnPath);
      return;
    }
    settingsReturnPath = currentAppPath();
    navigate("/settings");
  }

  const compactNavOptions = $derived.by(() => {
    const options: { value: CompactNavValue; label: string }[] = modeNavOptions
      .filter((option) => settings.isModeVisible(option.mode))
      .map(({ value, label }) => ({ value, label }));

    if (getPage() === "design-system") {
      options.push({ value: "design-system", label: "Design system" });
    }
    if (!isEmbedded() && getPage() === "settings") {
      options.push({ value: "settings", label: "Settings" });
    }

    return options;
  });
  const compactNavValue = $derived(
    getPage() === "pulls" && getView() === "board"
      ? "board"
      : getPage() === "terminal"
        ? "workspaces"
      : getPage(),
  );
  type StickyMode = "kata" | "docs" | "messages";
  const stickyModeDefaults: Record<StickyMode, string> = {
    kata: "/kata",
    docs: "/docs",
    messages: "/messages",
  };
  const lastStickyModeRoutes = new SvelteMap<StickyMode, string>();

  function stickyModeForPage(page: ReturnType<typeof getPage>): StickyMode | null {
    return page === "kata" || page === "docs" || page === "messages" ? page : null;
  }

  function rememberCurrentStickyModeRoute(): void {
    const currentMode = stickyModeForPage(getPage());
    if (!currentMode) return;
    lastStickyModeRoutes.set(currentMode, currentAppPath());
  }

  function routeForTab(
    destination: "pulls" | "issues",
  ): string {
    const selected = getPage() === "activity"
      ? parseActivitySelection(window.location.search)
      : null;
    return activitySelectionToRoute(selected, destination)
      ?? `/${destination}`;
  }

  function navigateTab(
    destination:
      | "activity"
      | "repos"
      | "kata"
      | "docs"
      | "messages"
      | "pulls"
      | "issues"
      | "board"
      | "reviews"
      | "workspaces"
      | "settings"
      | "design-system",
  ): void {
    const currentMode = stickyModeForPage(getPage());
    rememberCurrentStickyModeRoute();
    if (destination === "activity") navigate("/");
    else if (destination === "repos") navigate("/repos");
    else if (destination === "kata" || destination === "docs" || destination === "messages") {
      if (currentMode === destination) {
        lastStickyModeRoutes.set(destination, stickyModeDefaults[destination]);
        navigate(stickyModeDefaults[destination]);
        return;
      }
      navigate(lastStickyModeRoutes.get(destination) ?? stickyModeDefaults[destination]);
    }
    else if (destination === "pulls" || destination === "issues") {
      navigate(routeForTab(destination));
    } else if (destination === "board") navigate("/pulls/board");
    else if (destination === "reviews") navigate("/reviews");
    else if (destination === "workspaces") navigate("/workspaces");
    else if (destination === "settings") navigate("/settings");
    else if (destination === "design-system") navigate("/design-system");
  }

  function navigateCompactNav(value: string): void {
    if (value === "activity") navigateTab("activity");
    else if (value === "repos") navigateTab("repos");
    else if (value === "kata") navigateTab("kata");
    else if (value === "docs") navigateTab("docs");
    else if (value === "messages") navigateTab("messages");
    else if (value === "pulls") navigateTab("pulls");
    else if (value === "issues") navigateTab("issues");
    else if (value === "board") navigateTab("board");
    else if (value === "reviews") navigateTab("reviews");
    else if (value === "workspaces") navigateTab("workspaces");
    else if (value === "settings") navigateTab("settings");
    else if (value === "design-system") navigateTab("design-system");
  }

  function showMode(mode: ModeKey): boolean {
    return settings.isModeVisible(mode);
  }
</script>

<header class="app-header">
  <div class="header-left">
    {#if isSidebarCollapsed() && isSidebarToggleEnabled() && !hasSidebarStrip}
      <HeaderIconButton
        onclick={toggleSidebar}
        title="Expand sidebar"
      >
        <SidebarToggleIcon
          size="14"
          strokeWidth="1.5"
          aria-hidden="true"
        />
      </HeaderIconButton>
    {/if}
    <span class="brand">
      <img class="app-icon" src={appIconSrc} alt="" aria-hidden="true" />
      <span class="logo">middleman</span>
    </span>
    {#if showProviderRepoSelector}
      <RepoTypeahead
        selected={getGlobalRepo()}
        onchange={setGlobalRepo}
      />
    {/if}
    {#if compactHeader && !isNarrow()}
      <SelectDropdown
        class="nav-select"
        value={compactNavValue}
        options={compactNavOptions}
        onchange={navigateCompactNav}
        title="Page"
      />
    {/if}
  </div>

  <nav class="header-center">
    {#if compactHeader}
      {#if isNarrow()}
        <SelectDropdown
          class="nav-select"
          value={compactNavValue}
          options={compactNavOptions}
          onchange={navigateCompactNav}
          title="Page"
        />
      {/if}
    {:else}
      <div class="tab-group">
        {#if showMode("activity")}
          <button class="view-tab" class:active={getPage() === "activity"} onclick={() => { if (getPage() !== "activity") navigateTab("activity"); }}>
            Activity
          </button>
        {/if}
        {#if showMode("repos")}
          <button class="view-tab" class:active={getPage() === "repos"} onclick={() => navigateTab("repos")}>
            Repos
          </button>
        {/if}
        {#if showMode("kata")}
          <button class="view-tab" class:active={getPage() === "kata"} onclick={() => navigateTab("kata")}>
            Kata
          </button>
        {/if}
        {#if showMode("docs")}
          <button class="view-tab" class:active={getPage() === "docs"} onclick={() => navigateTab("docs")}>
            Docs
          </button>
        {/if}
        {#if showMode("messages")}
          <button class="view-tab" class:active={getPage() === "messages"} onclick={() => navigateTab("messages")}>
            Messages
          </button>
        {/if}
        {#if showMode("pulls")}
          <button class="view-tab" class:active={getPage() === "pulls"} onclick={() => navigateTab("pulls")}>
            PRs
          </button>
        {/if}
        {#if showMode("issues")}
          <button class="view-tab" class:active={getPage() === "issues"} onclick={() => navigateTab("issues")}>
            Issues
          </button>
        {/if}
        {#if showMode("board")}
          <button class="view-tab" class:active={getView() === "board"} onclick={() => navigateTab("board")}>
            Board
          </button>
        {/if}
        {#if showMode("reviews")}
          <button class="view-tab"
            class:active={getPage() === "reviews"}
            onclick={() => navigateTab("reviews")}>
            Reviews
            {#if stores.roborevDaemon && !stores.roborevDaemon.isAvailable()}
              <span class="daemon-indicator" title="Daemon unavailable"></span>
            {/if}
          </button>
        {/if}
        {#if showMode("workspaces")}
          <button
            class="view-tab"
            class:active={getPage() === "workspaces" || getPage() === "terminal"}
            onclick={() => navigateTab("workspaces")}
          >Workspaces</button>
        {/if}
      </div>
    {/if}
  </nav>

  <div class="header-right">
    <HeaderIconButton onclick={openPalette} title="Open command palette">
      <SearchIcon size="14" strokeWidth="1.75" aria-hidden="true" />
      <KbdBadge binding={{ key: "K", ctrlOrMeta: true }} />
    </HeaderIconButton>
    {#if !getUIConfig().hideSync}
      <button
        class="action-btn sync-btn"
        aria-label={syncing ? "Syncing" : "Sync"}
        title={syncing ? "Syncing" : "Sync"}
        onclick={handleSync}
        disabled={syncing}
      >
        {#if syncing}
          <span class="sync-icon sync-icon--spinning" aria-hidden="true">
            <SpinnerIcon
              size="14"
              strokeWidth="2"
            />
          </span>
        {:else}
          <span class="sync-icon" aria-hidden="true">
            <SyncIcon
              size="14"
              strokeWidth="1.75"
            />
          </span>
        {/if}
        <span class="sync-label">{syncing ? "Syncing..." : "Sync"}</span>
      </button>
    {/if}
    {#if isThemeToggleVisible()}
      <HeaderIconButton onclick={toggleTheme} title="Toggle theme">
        {#if isDark()}
          <SunIcon size="14" aria-hidden="true" />
        {:else}
          <span data-filled-icon="moon">
            <MoonIcon size="14" aria-hidden="true" />
          </span>
        {/if}
      </HeaderIconButton>
    {/if}
    {#if !isEmbedded()}
      <HeaderIconButton
        active={getPage() === "settings"}
        onclick={toggleSettings}
        title="Settings"
      >
        <SettingsIcon size="14" strokeWidth="1.75" aria-hidden="true" />
      </HeaderIconButton>
    {/if}
  </div>
</header>

<style>
  .app-header {
    height: var(--header-height);
    background: var(--bg-surface);
    border-bottom: 1px solid var(--border-default);
    display: flex;
    align-items: center;
    padding: 0 16px;
    gap: 16px;
    flex-shrink: 0;
    box-shadow: var(--shadow-sm);
  }

  .header-left {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .brand {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    flex-shrink: 0;
  }

  .app-icon {
    display: block;
    width: 22px;
    height: 22px;
  }

  .logo {
    font-weight: 600;
    font-size: var(--font-size-lg);
    color: var(--text-primary);
    letter-spacing: -0.01em;
  }

  .header-center {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .tab-group {
    display: flex;
    align-items: center;
    gap: 2px;
    background: var(--bg-inset);
    border-radius: var(--radius-md);
    padding: 2px;
  }

  .view-tab {
    padding: 4px 14px;
    border-radius: calc(var(--radius-md) - 2px);
    font-size: var(--font-size-md);
    font-weight: 500;
    color: var(--text-secondary);
    transition: background 0.15s, color 0.15s;
  }

  .view-tab:hover {
    color: var(--text-primary);
    background: var(--bg-surface-hover);
  }

  .view-tab.active {
    background: var(--bg-surface);
    color: var(--text-primary);
    box-shadow: var(--shadow-sm);
  }

  .header-right {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
  }

  .action-btn {
    box-sizing: border-box;
    height: 28px;
    padding: 5px 12px;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-md);
    font-weight: 500;
    color: var(--text-secondary);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    transition: background 0.15s, color 0.15s, border-color 0.15s;
  }

  .sync-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 7px;
    min-width: 34px;
    min-height: 28px;
    line-height: 0;
  }

  .sync-icon {
    display: inline-flex;
    flex-shrink: 0;
  }

  .sync-icon--spinning {
    animation: header-spin 0.9s linear infinite;
  }

  .sync-label {
    line-height: 1;
  }

  @keyframes header-spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }

  .action-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: var(--border-muted);
  }

  .action-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .daemon-indicator {
    display: inline-block;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text-muted);
    margin-left: 4px;
    vertical-align: middle;
    opacity: 0.6;
  }

  [data-filled-icon="moon"] :global(svg path) {
    fill: currentColor;
    stroke: none;
  }

  :global(#app.container-medium) .app-header {
    gap: 8px;
    padding-inline: 10px;
  }

  :global(#app.container-medium) .header-left {
    flex: 1 1 auto;
    gap: 8px;
  }

  :global(#app.container-medium) .header-left :global(.typeahead) {
    flex: 1 1 150px;
    min-width: 128px;
    max-width: 220px;
  }

  :global(#app.container-medium) .header-center {
    display: none;
  }

  :global(#app.container-medium) .header-left :global(.nav-select) {
    flex: 0 0 164px;
    min-width: 132px;
  }

  :global(#app.container-medium .nav-select) {
    width: 100%;
    min-width: 0;
  }

  :global(#app.container-medium .nav-select .select-dropdown-trigger) {
    border-color: var(--border-muted);
    background: var(--bg-inset);
  }

  :global(#app.container-medium) .header-right {
    flex: 0 0 auto;
    gap: 6px;
  }

  :global(#app.container-medium) .sync-btn {
    min-height: 28px;
    padding: 5px 10px;
  }

  :global(#app.container-medium) .sync-label {
    display: none;
  }

  :global(#app.container-narrow) .app-header {
    height: auto;
    min-height: 82px;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 8px;
    padding: 6px 10px;
  }

  :global(#app.container-narrow) .header-left {
    flex: 1 1 100%;
    gap: 8px;
    order: 1;
  }

  :global(#app.container-narrow) .brand {
    gap: 6px;
  }

  :global(#app.container-narrow) .app-icon {
    width: 20px;
    height: 20px;
  }

  :global(#app.container-narrow) .logo {
    font-size: var(--font-size-lg);
  }

  :global(#app.container-narrow) .header-left :global(.typeahead) {
    flex: 1 1 auto;
    min-width: 0;
    max-width: none;
  }

  :global(#app.container-narrow) .header-left :global(.typeahead-trigger),
  :global(#app.container-narrow) .header-left :global(.typeahead-input) {
    height: 30px;
  }

  :global(#app.container-narrow) .header-center {
    flex: 1 1 min(190px, 100%);
    min-width: 0;
    order: 2;
  }

  :global(#app.container-narrow .nav-select) {
    width: 100%;
    min-width: 0;
  }

  :global(#app.container-narrow .nav-select .select-dropdown-trigger) {
    min-height: 32px;
    font-size: var(--font-size-md);
  }

  :global(#app.container-narrow) .header-right {
    flex: 0 0 auto;
    order: 3;
    gap: 6px;
  }

  :global(#app.container-narrow) .action-btn {
    height: 32px;
    padding-inline: 10px;
  }

  :global(#app.container-narrow) .sync-label {
    display: none;
  }
</style>
