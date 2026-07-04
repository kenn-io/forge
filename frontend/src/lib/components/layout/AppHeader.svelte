<script lang="ts">
  import { TopBar, type TopBarTab } from "@kenn-io/kit-ui";
  import { getStores, KbdBadge } from "@middleman/ui";
  import type { ModeVisibility } from "@middleman/ui/api/types";
  import { SvelteMap } from "svelte/reactivity";
  import {
    getBasePath,
    getLastActivityRoute,
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
  import ThemeToggle from "./ThemeToggle.svelte";
  import {
    SearchIcon,
    SettingsIcon,
    SidebarToggleIcon,
    SpinnerIcon,
    SyncIcon,
  } from "../../icons.ts";
  import { getGlobalRepo, setGlobalRepo } from "../../stores/filter.svelte.js";
  import { isEmbedded, getUIConfig } from "../../stores/embed-config.svelte.js";
  import { isThemeToggleVisible } from "../../stores/theme.svelte.js";
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
  type NavValue = NavDestination | "settings" | "design-system";

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
  const hideProviderRepoSelector = $derived(getUIConfig().hideRepoSelector);
  const isProviderRepoSelectorPage = $derived(
    getPage() === "activity" ||
      getPage() === "repos" ||
      getPage() === "pulls" ||
      getPage() === "issues",
  );
  const showProviderRepoSelector = $derived(!hideProviderRepoSelector && isProviderRepoSelectorPage);
  const reserveProviderRepoSelectorSlot = $derived(!hideProviderRepoSelector && !isProviderRepoSelectorPage);
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

  // Settings and the design-system gallery are not modes, but while one of
  // those pages is current it needs a tab entry: the collapsed dropdown
  // otherwise presents the first mode as the current page.
  const tabs: TopBarTab[] = $derived.by(() => {
    const entries: TopBarTab[] = modeNavOptions
      .filter((option) => settings.isModeVisible(option.mode))
      .map(({ value, label }) => ({ id: value, label }));

    if (getPage() === "design-system") {
      entries.push({ id: "design-system", label: "Design system" });
    }
    if (!isEmbedded() && getPage() === "settings") {
      entries.push({ id: "settings", label: "Settings" });
    }

    return entries;
  });

  const routeTabId = $derived(
    getPage() === "pulls" && getView() === "board"
      ? "board"
      : getPage() === "terminal"
        ? "workspaces"
        : getPage() === "repo-browser"
          ? "repos"
      : getPage(),
  );
  // The route owns the active tab: TopBar writes a click into the binding,
  // navigation happens through onchange, and this sync settles the binding
  // on whatever page the router actually landed on.
  let activeTab = $state("");
  let tabsCollapsed = $state(false);
  $effect(() => {
    activeTab = routeTabId;
  });

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

  function navigateTab(destination: NavValue): void {
    const currentMode = stickyModeForPage(getPage());
    rememberCurrentStickyModeRoute();
    if (destination === "activity") {
      if (getPage() !== "activity") navigate(getLastActivityRoute());
    }
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

  function handleTabChange(value: string): void {
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

  const showReviewsDaemonIndicator = $derived(
    settings.isModeVisible("reviews")
      && stores.roborevDaemon !== undefined
      && !stores.roborevDaemon.isAvailable(),
  );
</script>

<!-- The app header renders through kit TopBar; app-top-bar is the app-owned
     selector alias (the kit element also carries .kit-top-bar) used by the
     app-startup/focus/embedded/routing specs to assert header presence. -->
<TopBar
  class="app-top-bar"
  {tabs}
  bind:active={activeTab}
  bind:collapsed={tabsCollapsed}
  centerTabs
  ariaLabel="Page"
  onchange={handleTabChange}
>
  {#snippet left()}
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
    {:else if reserveProviderRepoSelectorSlot}
      <div
        class="typeahead repo-selector-placeholder"
        aria-hidden="true"
      ></div>
    {/if}
  {/snippet}

  {#snippet right()}
    {#if showReviewsDaemonIndicator}
      <!-- Lives here rather than on the Reviews tab: kit TopBarTab has no
           indicator affordance yet (kata kit-ui#b3zf); the dot must also
           survive the tabs collapsing into the dropdown. role="img" +
           aria-label name it for AT since it is detached from the Reviews tab
           and a bare title on a non-interactive span announces unreliably. -->
      <span
        class="daemon-indicator"
        role="img"
        aria-label="Reviews daemon unavailable"
        title="Daemon unavailable"
      ></span>
    {/if}
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
        {#if !tabsCollapsed}
          <span class="sync-label">{syncing ? "Syncing..." : "Sync"}</span>
        {/if}
      </button>
    {/if}
    {#if isThemeToggleVisible()}
      <ThemeToggle />
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
  {/snippet}
</TopBar>

<style>
  .brand {
    display: inline-flex;
    align-items: center;
    gap: var(--space-3);
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
    gap: var(--space-3);
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

  .repo-selector-placeholder {
    display: block;
    height: 26px;
    pointer-events: none;
    visibility: hidden;
  }

  /* Busy state spins the sync affordance icon itself, matching kit-ui
     RefreshControl's pattern; the sync button carries app-specific
     label/disable semantics RefreshControl does not model. */
  /* kit-ui-check-ignore: RefreshControl-style icon spin on an app button */
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
    margin-right: 2px;
    opacity: 0.6;
  }

  /* kit's collapse probe is a position:absolute row that always renders the
     full (uncollapsed) tab labels to measure their natural width. With no
     clip it extends past the bar and inflates body scrollWidth, producing
     horizontal page overflow at narrow widths. Clip the x-axis only: the
     nav and typeahead dropdowns open downward and must stay visible, and the
     probe's own offsetWidth (what kit measures) is unaffected by the clip. */
  :global(.app-top-bar) {
    overflow-x: clip;
  }

  /* Region sizing on kit's bar: the side regions never shrink (kit collapses
     the tabs first), so the repo typeahead gets an app-side width cap to keep
     the left region honest in tighter containers. */
  :global(.kit-top-bar .kit-top-bar__left .typeahead) {
    flex: 1 1 150px;
    min-width: 128px;
    max-width: 220px;
  }

  :global(#app.container-medium .kit-top-bar) {
    gap: 8px;
    padding-inline: 10px;
  }

  :global(.kit-top-bar .kit-top-bar__nav-select .kit-select-dropdown__trigger) {
    border-color: var(--border-muted);
    background: var(--bg-inset);
  }

  /* Narrow containers (embedded or split panes under 500px) keep the
     two-row header: the left region wraps onto the first row and the
     collapsed nav dropdown shares the second row with the action buttons.
     kit's measurement keeps the tabs collapsed here — the wrap only reorders
     the regions it renders.

     The left region is content-sized (flex: 0 1 auto) rather than stretched
     to a full row: a stretched left inflated the side-region footprint kit
     freezes into expandUsed at collapse time, which then blocked the tabs
     from ever re-expanding when the container widened. Content-sizing keeps
     kit's collapse math honest across the narrow->wide transition. */
  :global(#app.container-narrow .kit-top-bar) {
    height: auto;
    min-height: 82px;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 8px;
    padding: 6px 10px;
  }

  :global(#app.container-narrow .kit-top-bar .kit-top-bar__left) {
    flex: 0 1 auto;
    order: 1;
    gap: 8px;
  }

  :global(#app.container-narrow .kit-top-bar) .brand {
    gap: 6px;
  }

  :global(#app.container-narrow .kit-top-bar) .app-icon {
    width: 20px;
    height: 20px;
  }

  :global(#app.container-narrow .kit-top-bar .kit-top-bar__left .typeahead) {
    flex: 1 1 auto;
    min-width: 0;
    max-width: none;
  }

  :global(#app.container-narrow .kit-top-bar .kit-top-bar__left .typeahead-trigger),
  :global(#app.container-narrow .kit-top-bar .kit-top-bar__left .typeahead-input) {
    height: 30px;
  }

  :global(#app.container-narrow .kit-top-bar .kit-top-bar__nav) {
    flex: 1 1 min(190px, 100%);
    min-width: 0;
    order: 2;
  }

  :global(#app.container-narrow .kit-top-bar .kit-top-bar__nav-select) {
    width: 100%;
    min-width: 0;
  }

  :global(#app.container-narrow .kit-top-bar .kit-top-bar__nav-select .kit-select-dropdown__trigger) {
    min-height: 32px;
    font-size: var(--font-size-md);
  }

  :global(#app.container-narrow .kit-top-bar .kit-top-bar__right) {
    flex: 0 0 auto;
    order: 3;
    margin-left: 0;
    gap: 6px;
  }

  /* Every right-region control gets the same narrow-height bump, not just the
     sync .action-btn: the hand-rolled sync button and the HeaderIconButton
     controls (palette, theme, settings) sit side by side, so bumping one
     alone leaves them misaligned — and mismatched mid-transition, since kit
     re-expands the tabs immediately while the container class drops on a
     debounce. Governing all of them by the same class keeps their heights
     equal in every state. */
  :global(#app.container-narrow .kit-top-bar .kit-top-bar__right button) {
    height: 32px;
    padding-inline: 10px;
  }
</style>
