<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import {
    Provider,
    PRListView,
    IssueListView,
    ActivityFeedView,
    MobileActivityView,
    KanbanBoardView,
    ReviewsView,
    FocusListView,
  } from "@middleman/ui";
  import type { StoreInstances } from "@middleman/ui";
  import type { ActivityItem } from "@middleman/ui/api/types";
  import {
    buildFocusPullRequestFilesRoute,
    buildFocusPullRequestRoute,
    buildRoutedItemRoute,
    type PullRequestRouteRef,
    type RoutedItemRef,
  } from "@middleman/ui/routes";
  import { client } from "./lib/api/runtime.js";

  import AppHeader from "./lib/components/layout/AppHeader.svelte";
  import StatusBar from "./lib/components/layout/StatusBar.svelte";
  import Palette from "./lib/components/keyboard/Palette.svelte";
  import Cheatsheet from "./lib/components/keyboard/Cheatsheet.svelte";
  import RepoSummaryPage from "./lib/components/repositories/RepoSummaryPage.svelte";
  import SettingsPage from "./lib/components/settings/SettingsPage.svelte";
  import WorkspaceTerminalView from "./lib/components/terminal/WorkspaceTerminalView.svelte";
  import WorkspaceEmbedShell from "./lib/components/terminal/WorkspaceEmbedShell.svelte";
  import DesignSystemPage from "./lib/components/design-system/DesignSystemPage.svelte";
  import FlashBanner from "./lib/components/FlashBanner.svelte";
  import { MonitorIcon, SpinnerIcon } from "./lib/icons.ts";
  import { showFlash } from "./lib/stores/flash.svelte.js";
  import { initItemRefHandler } from "./lib/utils/itemRefHandler.js";
  import { globalRepoForSelectedRoute } from "./lib/utils/repoSelectionSync.js";
  import { runAppStartup } from "./lib/utils/appStartup.js";
  import {
    initTheme,
    cleanupTheme,
    reapplyTheme,
  } from "./lib/stores/theme.svelte.js";
  import {
    isSidebarCollapsed,
    getSidebarWidth,
    setSidebarWidth,
    toggleSidebar,
    isSidebarToggleEnabled,
    initSidebar,
    setNarrowOverride,
  } from "./lib/stores/sidebar.svelte.js";
  import {
    initContainerObserver,
    isNarrow,
  } from "./lib/stores/container.svelte.js";
  import {
    getRoute,
    getPage,
    getView,
    navigate,
    replaceUrl,
    getBasePath,
    isMobilePage,
    getDetailTab,
    getSelectedPRFromRoute,
  } from "./lib/stores/router.svelte.ts";
  import {
    buildActivitySelectionSearch,
    parseActivitySelection,
    type ActivityDetailTab,
  } from "./lib/utils/activitySelection.js";
  import {
    getGlobalRepo,
    applyConfigRepo,
    setGlobalRepo,
    parseRepoFilterValue,
  } from "./lib/stores/filter.svelte.js";
  import {
    getUIConfig,
    isEmbedded,
    getPullRequestActions,
    getIssueActions,
    getActiveWorktreeKey,
    invokeAction,
    emitWorkspaceCommand,
    isHeaderHidden,
    isStatusBarHidden,
    emitLayoutChanged,
    initWorkspaceBridge,
  } from "./lib/stores/embed-config.svelte.js";
  import { getSettings } from "./lib/api/settings.js";
  import { shouldUseFullAppShell } from "./lib/utils/appShell.js";
  import { registerScopedActions } from "./lib/stores/keyboard/registry.svelte.js";
  import {
    defaultActions,
    setStoreInstances,
  } from "./lib/stores/keyboard/actions.js";
  import { dispatchKeydown } from "./lib/stores/keyboard/dispatch.svelte.js";
  import { buildContext } from "./lib/stores/keyboard/context.svelte.js";
  import { registerPRDetailActions } from "./lib/stores/keyboard/pr-detail-actions.js";
  import type { PRDetailActionInput } from "../../packages/ui/src/components/detail/keyboard-actions.js";
  import type { Context } from "./lib/stores/keyboard/types.js";

  let stores = $state<StoreInstances | undefined>();
  let appReady = $state(false);
  let viewportWidth = $state(window.innerWidth);
  let hasCoarsePointer = $state(window.matchMedia("(pointer: coarse)").matches);
  let cleanupFullAppShell: (() => void) | undefined;
  let fullShellStores: StoreInstances | undefined;
  const appIconSrc = `${getBasePath().replace(/\/$/, "")}/favicon.svg`;

  function stopFullAppShell() {
    fullShellStores?.events.disconnect();
    cleanupFullAppShell?.();
    cleanupFullAppShell = undefined;
    fullShellStores = undefined;
    appReady = false;
  }

  function syncGlobalRepoWithRoute(
    routeStores: StoreInstances | undefined = stores,
  ): void {
    if (!routeStores) return;
    if (getUIConfig().hideRepoSelector) return;
    if (!routeStores.settings.hasConfiguredRepos()) return;
    const currentRepo = untrack(getGlobalRepo);
    if (currentRepo === undefined) return;
    if (parseRepoFilterValue(currentRepo).length !== 1) return;
    const next = globalRepoForSelectedRoute(getRoute());
    if (next === undefined) return;
    if (currentRepo === next) return;
    setGlobalRepo(next);
  }

  function startFullAppShell(startupStores: StoreInstances) {
    if (cleanupFullAppShell) return;
    fullShellStores = startupStores;
    appReady = false;
    initTheme();
    initSidebar();
    initWorkspaceBridge();
    const ui = getUIConfig();
    applyConfigRepo(ui.repo, ui.hideRepoSelector);
    const appEl = document.getElementById("app")!;
    const cleanupContainer = initContainerObserver(appEl);
    const cleanupItemRefs = initItemRefHandler();
    const cancelStartup = runAppStartup({
      getSettings,
      getStores: () => startupStores,
      beforeInitialLoad: () => syncGlobalRepoWithRoute(startupStores),
      onReady: () => {
        appReady = true;
      },
    });
    const onBeforeUnload = () => {
      stores?.events.disconnect();
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    cleanupFullAppShell = () => {
      cancelStartup();
      cleanupTheme();
      cleanupContainer();
      cleanupItemRefs();
      window.removeEventListener(
        "beforeunload",
        onBeforeUnload,
      );
    };
  }

  $effect(() => {
    if (!shouldUseFullAppShell(getPage())) {
      stopFullAppShell();
      return;
    }
    if (stores && stores !== fullShellStores) {
      stopFullAppShell();
      startFullAppShell(stores);
    }
  });

  let lastRepo: string | undefined;

  function searchWithDesktopOptOut(): string {
    const params = new URLSearchParams(window.location.search);
    params.set("desktop", "1");
    const text = params.toString();
    return text ? `?${text}` : "?desktop=1";
  }

  function updateViewportState(): void {
    viewportWidth = window.innerWidth;
    hasCoarsePointer = window.matchMedia("(pointer: coarse)").matches;
  }

  function hasMobileUserAgent(): boolean {
    return /\b(Android|iPhone|iPod|IEMobile|Mobile)\b/i.test(navigator.userAgent);
  }

  function isPhoneLikeViewport(): boolean {
    return viewportWidth <= 640
      && (hasCoarsePointer || hasMobileUserAgent());
  }

  function isCompactViewport(): boolean {
    const hasNarrowContainer = isNarrow();
    return viewportWidth <= 640 || hasNarrowContainer || isPhoneLikeViewport();
  }

  function shouldUseDesktopOnPhone(): boolean {
    return new URLSearchParams(window.location.search).get("desktop") === "1";
  }

  function shouldForceMobileRoutes(): boolean {
    return (
      window.__MIDDLEMAN_FORCE_MOBILE_ROUTES__ === true ||
      import.meta.env.VITE_MIDDLEMAN_FORCE_MOBILE_ROUTES === "1" ||
      import.meta.env.VITE_MIDDLEMAN_FORCE_MOBILE_ROUTES === "true"
    );
  }

  function shouldUseResponsiveFocusPresentation(): boolean {
    const route = getRoute();
    if (shouldUseDesktopOnPhone()) return false;
    if (!isCompactViewport() && !shouldForceMobileRoutes()) return false;
    if (route.page === "pulls") return route.view === "list";
    return route.page === "issues";
  }

  function shouldUseFocusPresentation(): boolean {
    return getPage() === "focus" || shouldUseResponsiveFocusPresentation();
  }

  function useFocusLayoutClass(): boolean {
    return isPhoneLikeViewport() || shouldForceMobileRoutes();
  }

  function shouldUseResponsiveMobileActivityPresentation(): boolean {
    if (shouldUseDesktopOnPhone()) return false;
    if (getPage() !== "activity") return false;
    return isCompactViewport() || shouldForceMobileRoutes();
  }

  function navigateFocusPRDetailTab(
    ref: Parameters<typeof buildFocusPullRequestRoute>[0],
    tab: "conversation" | "files",
  ): void {
    navigate(
      tab === "files"
        ? buildFocusPullRequestFilesRoute(ref)
        : buildFocusPullRequestRoute(ref),
    );
  }

  function desktopPathForMobileRoute(): string {
    const page = getPage();
    if (page === "mobile-pulls") return "/pulls";
    if (page === "mobile-issues") return "/issues";
    return "/";
  }

  function navigateMobile(path: string): void {
    navigate(`${path}${window.location.search}`);
  }

  function useDesktopView(): void {
    replaceUrl(`${desktopPathForMobileRoute()}${searchWithDesktopOptOut()}`);
  }

  onDestroy(() => {
    stopFullAppShell();
    stores?.events.disconnect();
  });

  $effect(() => {
    const repo = getGlobalRepo();
    if (!appReady || !stores) {
      lastRepo = repo;
      return;
    }
    if (repo === lastRepo) return;
    lastRepo = repo;
    void stores.pulls.loadPulls(
      getView() === "board" ? { state: "open" } : undefined,
    );
    void stores.issues.loadIssues();
    void stores.activity.loadActivity();
  });

  $effect(() => {
    if (isSidebarToggleEnabled()) {
      setNarrowOverride(isNarrow());
    }
  });

  $effect(() => {
    if (!shouldUseFullAppShell(getPage())) return;
    reapplyTheme();
  });

  // Sync route state: restore drawer, select items, clear stale.
  $effect(() => {
    if (!stores) return;
    const route = getRoute();
    const page = route.page;

    if (page !== "activity") {
      drawerItem = null;
    } else if (!stores.settings.hasConfiguredRepos()) {
      drawerItem = null;
    } else {
      const nextDrawer = parseActivitySelection(
        window.location.search,
      );
      if (!sameActivitySelection(drawerItem, nextDrawer)) {
        drawerItem = nextDrawer;
      }
    }

    if (route.page === "pulls") {
      if (
        "selected" in route &&
        route.selected &&
        stores.settings.hasConfiguredRepos()
      ) {
        stores.pulls.selectPR(
          route.selected.owner,
          route.selected.name,
          route.selected.number,
          route.selected.provider,
          route.selected.platformHost,
          route.selected.repoPath,
        );
      } else {
        stores.pulls.clearSelection();
      }
    } else if (route.page === "issues") {
      if (
        route.selected &&
        stores.settings.hasConfiguredRepos()
      ) {
        stores.issues.selectIssue(
          route.selected.owner,
          route.selected.name,
          route.selected.number,
          route.selected.provider,
          route.selected.platformHost,
          route.selected.repoPath,
        );
      } else {
        stores.issues.clearIssueSelection();
      }
    }
  });

  // Keep the repo dropdown and sidebar list aligned with the item in
  // the URL. Without this, navigating to a PR/issue link leaves the
  // dropdown and left list pinned to whichever repo was picked before,
  // even though the detail pane jumped to a different one.
  $effect(() => {
    syncGlobalRepoWithRoute();
  });

  type DrawerItem = RoutedItemRef & {
    detailTab: ActivityDetailTab;
  };

  let drawerItem = $state<DrawerItem | null>(null);

  function sameActivitySelection(
    left: DrawerItem | null,
    right: DrawerItem | null,
  ): boolean {
    if (left === right) return true;
    if (left === null || right === null) return false;
    return left.itemType === right.itemType
      && left.provider === right.provider
      && left.platformHost === right.platformHost
      && left.repoPath === right.repoPath
      && left.owner === right.owner
      && left.name === right.name
      && left.number === right.number
      && left.detailTab === right.detailTab;
  }

  function updateDrawerURL(
    item: DrawerItem | null,
  ): void {
    if (getPage() !== "activity") return;
    const sp = buildActivitySelectionSearch(
      window.location.search,
      item,
    );
    const qs = sp.toString();
    replaceUrl(qs ? `/?${qs}` : "/");
  }

  function handleActivitySelect(
    item: ActivityItem,
  ): void {
    if (!item.repo) {
      throw new Error("activity item missing provider repo identity");
    }
    const itemType =
      item.item_type === "issue" ? "issue" : "pr";
    const selectedItem = {
      itemType,
      provider: item.repo.provider,
      platformHost: item.repo.platform_host,
      repoPath: item.repo.repo_path,
      owner: item.repo.owner,
      name: item.repo.name,
      number: item.item_number,
    } satisfies RoutedItemRef;

    if (isMobilePage(getPage()) || shouldUseResponsiveMobileActivityPresentation()) {
      navigate(buildRoutedItemRoute(selectedItem, { focus: true }));
      return;
    }

    drawerItem = {
      ...selectedItem,
      detailTab: "conversation",
    };
    updateDrawerURL(drawerItem);
  }

  function handleActivityDetailTabChange(
    tab: "conversation" | "files",
  ): void {
    if (!drawerItem || drawerItem.itemType !== "pr") return;
    drawerItem = { ...drawerItem, detailTab: tab };
    updateDrawerURL(drawerItem);
  }

  function handleActivityDrawerItemChange(
    item: DrawerItem,
  ): void {
    drawerItem = item;
    updateDrawerURL(drawerItem);
  }

  function handleResponsiveStackMemberNavigate(
    ref: PullRequestRouteRef,
  ): boolean | void {
    if (shouldUseResponsiveFocusPresentation()) {
      navigate(buildFocusPullRequestRoute(ref));
      return true;
    }
    return undefined;
  }

  function closeDrawer(): void {
    drawerItem = null;
    updateDrawerURL(null);
  }

  function handleSidebarResize(width: number): void {
    setSidebarWidth(width);
    emitLayoutChanged({
      sidebar: { width },
      pinnedPanel: { width: 0, visible: false },
    });
  }

  $effect(() => {
    if (!shouldUseFullAppShell(getPage())) return;
    if (!stores) return;
    setStoreInstances(() => stores!);
    const cleanupDefaults = registerScopedActions("app:defaults", defaultActions);
    // Activity-page drawer close is owned by App.svelte because drawerItem and
    // closeDrawer are local to this component. Mirrors the pre-migration
    // behavior where Escape on the activity page closed the open PR drawer.
    const cleanupActivity = registerScopedActions("app:activity-drawer", [
      {
        id: "activity.drawer.close",
        label: "Close activity drawer",
        scope: "global",
        binding: { key: "Escape" },
        priority: 50,
        when: (ctx) => ctx.page === "activity" && drawerItem !== null,
        handler: () => closeDrawer(),
      },
    ]);
    const onKeydown = (e: KeyboardEvent) =>
      dispatchKeydown(e, () => buildContext(stores!));
    window.addEventListener("keydown", onKeydown);
    return () => {
      window.removeEventListener("keydown", onKeydown);
      cleanupActivity();
      cleanupDefaults();
    };
  });

  // PR-detail palette commands (pr.approve, pr.ready, pr.approveWorkflows).
  // Lives here in the app shell because the keyboard registry can't be
  // imported from inside @middleman/ui. The buildPRDetailInput closure
  // assembles the action input from the active PR detail, the loaded
  // capabilities, and the app stores; it returns null when nothing is
  // ready, in which case every action's `when` returns false. pr.merge
  // is intentionally NOT wired (see pr-detail-actions.ts).
  function buildPRDetailInput(ctx: Context): PRDetailActionInput | null {
    if (!stores) return null;
    if (ctx.selectedPR === null) return null;
    const detail = stores.detail.getDetail();
    if (detail === null) return null;
    const sel = ctx.selectedPR;
    // Palette actions only apply to the PR that is actually loaded in
    // the detail pane. If the route-derived selection is for a different
    // PR (mid-route-change, deep link not yet resolved), we treat the
    // input as not ready so `when` returns false.
    const stale =
      detail.repo_owner !== sel.owner
      || detail.repo_name !== sel.name
      || (detail.merge_request?.Number ?? -1) !== sel.number
      || detail.repo?.provider !== sel.provider
      || detail.repo?.platform_host !== sel.platformHost
      || detail.repo?.repo_path !== sel.repoPath;
    if (stale) return null;
    const pr = detail.merge_request;
    const capabilities = detail.repo?.capabilities;
    if (!pr || !capabilities) return null;
    const wfa = detail.workflow_approval;
    const workflowApprovalReady = Boolean(
      capabilities.workflow_approval && wfa?.checked && wfa.required,
    );
    return {
      pr: {
        State: pr.State,
        IsDraft: pr.IsDraft,
        MergeableState: pr.MergeableState,
      },
      ref: {
        provider: sel.provider,
        platformHost: sel.platformHost,
        owner: sel.owner,
        name: sel.name,
        repoPath: sel.repoPath,
      },
      number: sel.number,
      viewerCan: {
        approve: capabilities.review_mutation,
        merge: capabilities.merge_mutation,
        markReady: capabilities.ready_for_review,
        approveWorkflows: workflowApprovalReady,
      },
      // pr.merge is not registered, so repoSettings is not consulted.
      repoSettings: null,
      // Same identity check feeds `stale`; reaching this return means
      // selection and detail agree, so the action is fresh.
      stale: false,
      stores: { pulls: stores.pulls, detail: stores.detail },
      client,
      approveCommentBody: "",
      onError: (msg: string) => showFlash(msg),
    };

  }

  $effect(() => {
    if (!stores) return;
    return registerPRDetailActions(buildPRDetailInput);
  });
</script>

<svelte:window onresize={updateViewportState} />

{#if !shouldUseFullAppShell(getPage())}
  <WorkspaceEmbedShell />
{:else}
  <Provider
    {client}
    roborevBaseUrl="/api/roborev"
    onError={showFlash}
    onNavigate={(e) =>
      navigate(typeof e === "string" ? e : e.path)}
    onWorkspaceCommand={emitWorkspaceCommand}
    actions={{
      pull: getPullRequestActions().map((a) => ({
        id: a.id,
        label: a.label,
        handler: (ctx) => invokeAction(a, {
          surface: ctx.surface,
          owner: ctx.owner,
          name: ctx.name,
          number: ctx.number,
          ...ctx.meta != null && { meta: ctx.meta },
        }),
      })),
      issue: getIssueActions().map((a) => ({
        id: a.id,
        label: a.label,
        handler: (ctx) => invokeAction(a, {
          surface: ctx.surface,
          owner: ctx.owner,
          name: ctx.name,
          number: ctx.number,
          ...ctx.meta != null && { meta: ctx.meta },
        }),
      })),
    }}
    hostState={{
      getGlobalRepo,
      getGroupByRepo: () => stores?.grouping.getGroupByRepo() ?? true,
      getView,
      getActiveWorktreeKey,
    }}
    config={{
      hideStar: getUIConfig().hideStar,
      basePath: getBasePath(),
    }}
    {getPage}
    sidebar={{
      isEmbedded,
      isSidebarToggleEnabled,
      toggleSidebar,
    }}
    bind:stores
  >
  {#if shouldUseFocusPresentation()}
    {@const r = getRoute()}
    <main
      class="focus-layout"
      class:focus-layout--phone={useFocusLayoutClass()}
    >
      {#if r.page === "focus" && r.itemType === "mrs"}
        <FocusListView
          listType="mrs"
          {...r.repo ? { repo: r.repo } : {}}
        />
      {:else if r.page === "focus" && r.itemType === "issues"}
        <FocusListView
          listType="issues"
          {...r.repo ? { repo: r.repo } : {}}
        />
      {:else if r.page === "focus" && r.itemType === "pr"}
        {@const selectedPR = {
          owner: r.owner,
          name: r.name,
          number: r.number,
          provider: r.provider,
          platformHost: r.platformHost,
          repoPath: r.repoPath,
        }}
        <PRListView
          {selectedPR}
          detailTab={r.tab === "files" ? "files" : "conversation"}
          onDetailTabChange={(tab) => navigateFocusPRDetailTab(selectedPR, tab)}
          isSidebarCollapsed={true}
          hideSidebar={true}
          routeFamily="focus"
        />
      {:else if r.page === "focus"}
        <IssueListView
          selectedIssue={{
            owner: r.owner,
            name: r.name,
            number: r.number,
            provider: r.provider,
            platformHost: r.platformHost,
            repoPath: r.repoPath,
          }}
          isSidebarCollapsed={true}
          hideSidebar={true}
        />
      {:else if r.page === "pulls" && r.selected}
        <PRListView
          selectedPR={r.selected}
          detailTab={r.tab === "files" ? "files" : "conversation"}
          isSidebarCollapsed={true}
          hideSidebar={true}
          onStackMemberNavigate={handleResponsiveStackMemberNavigate}
        />
      {:else if r.page === "pulls"}
        <FocusListView
          listType="mrs"
          routeFamily="canonical"
        />
      {:else if r.page === "issues" && r.selected}
        <IssueListView
          selectedIssue={r.selected}
          isSidebarCollapsed={true}
          hideSidebar={true}
        />
      {:else if r.page === "issues"}
        <FocusListView
          listType="issues"
          routeFamily="canonical"
        />
      {/if}
    </main>
  {:else if isMobilePage(getPage()) || shouldUseResponsiveMobileActivityPresentation()}
    <section class="mobile-shell" aria-label="Phone view">
      <header class="mobile-topbar">
        <span class="mobile-brand">
          <img class="mobile-app-icon" src={appIconSrc} alt="" aria-hidden="true" />
          <span class="mobile-title">middleman</span>
        </span>

        <nav class="mobile-tabs" aria-label="Phone navigation">
          <a
            class:mobile-tab--active={getPage() === "mobile-activity" || getPage() === "activity"}
            href="/m"
            onclick={(e) => {
              e.preventDefault();
              navigateMobile("/m");
            }}
          >Activity</a>
          <a
            class:mobile-tab--active={getPage() === "mobile-pulls"}
            href="/m/pulls"
            onclick={(e) => {
              e.preventDefault();
              navigateMobile("/m/pulls");
            }}
          >PRs</a>
          <a
            class:mobile-tab--active={getPage() === "mobile-issues"}
            href="/m/issues"
            onclick={(e) => {
              e.preventDefault();
              navigateMobile("/m/issues");
            }}
          >Issues</a>
        </nav>

        <button
          class="mobile-desktop-link"
          type="button"
          aria-label="Open desktop view"
          title="Open desktop view"
          onclick={useDesktopView}
        >
          <MonitorIcon size="18" strokeWidth="1.75" aria-hidden="true" />
        </button>
      </header>

      <main class="mobile-main">
        {#if !appReady}
          <div class="loading-state">
            <SpinnerIcon
              class="loading-spinner"
              size="18"
              strokeWidth="2"
              aria-hidden="true"
            />
            Loading
          </div>
        {:else if getPage() === "mobile-pulls"}
          <FocusListView listType="mrs" />
        {:else if getPage() === "mobile-issues"}
          <FocusListView listType="issues" />
        {:else}
          <MobileActivityView
            selectedRepo={getGlobalRepo()}
            onRepoChange={setGlobalRepo}
            onSelectItem={handleActivitySelect}
          />
        {/if}
      </main>
    </section>
  {:else}
    {#if !isHeaderHidden()}
      <AppHeader />
    {/if}
    <FlashBanner />

    <main class="app-main">
      {#if getPage() === "design-system"}
        <DesignSystemPage />
      {:else if !appReady}
        <div class="loading-state">
          <SpinnerIcon
            class="loading-spinner"
            size="18"
            strokeWidth="2"
            aria-hidden="true"
          />
          Loading
        </div>
      {:else if getPage() === "settings"}
        <SettingsPage />
      {:else if getPage() === "activity"}
        <ActivityFeedView
          {drawerItem}
          onSelectItem={handleActivitySelect}
          onCloseDrawer={closeDrawer}
          detailTab={drawerItem?.detailTab ?? "conversation"}
          onDetailTabChange={handleActivityDetailTabChange}
          onDrawerItemChange={handleActivityDrawerItemChange}
        />
      {:else if getPage() === "repos"}
        <RepoSummaryPage />
      {:else if getPage() === "pulls"}
        {@const route = getRoute()}
        {#if route.page === "pulls" && route.view === "board"}
          <KanbanBoardView />
        {:else}
          {@const selectedPR =
            getSelectedPRFromRoute() ??
            stores?.pulls.getSelectedPR() ??
            null}
          {@const detailTab = getDetailTab()}
          <PRListView
            {selectedPR}
            {detailTab}
            isSidebarCollapsed={isSidebarCollapsed()}
            sidebarWidth={getSidebarWidth()}
            onSidebarResize={handleSidebarResize}
          />
        {/if}
      {:else if getPage() === "issues"}
        {@const selectedIssue =
          stores?.issues.getSelectedIssue() ?? null}
          <IssueListView
            {selectedIssue}
          isSidebarCollapsed={isSidebarCollapsed()}
          sidebarWidth={getSidebarWidth()}
          onSidebarResize={handleSidebarResize}
        />
      {:else if getPage() === "reviews"}
        {@const route = getRoute()}
        {#if route.page === "reviews" && route.jobId != null}
          <ReviewsView jobId={route.jobId} />
        {:else}
          <ReviewsView />
        {/if}
      {:else if getPage() === "workspaces" || getPage() === "terminal"}
        {@const r = getRoute()}
        {@const wsId =
          r.page === "terminal" ? r.workspaceId : ""}
        <!-- Single mount across /workspaces and /terminal/{id};
             WorkspaceTerminalView reacts to workspaceId changes
             internally so the page doesn't flash on navigation. -->
        <WorkspaceTerminalView
          workspaceId={wsId}
          isSidebarCollapsed={isSidebarCollapsed()}
          sidebarWidth={getSidebarWidth()}
          onSidebarResize={handleSidebarResize}
          isSidebarToggleEnabled={isSidebarToggleEnabled()}
          onToggleSidebar={toggleSidebar}
        />
      {/if}
    </main>

    {#if !isStatusBarHidden()}
      <StatusBar />
    {/if}
  {/if}

    <Palette />
    <Cheatsheet />
  </Provider>
{/if}

<style>
  .mobile-shell {
    --mobile-type-xs: var(--font-size-mobile-xs, 1.08rem);
    --mobile-type-sm: var(--font-size-mobile-sm, 1.17rem);
    --mobile-type-body: var(--font-size-mobile-body, 1.24rem);
    --mobile-type-title: var(--font-size-mobile-title, 1.54rem);
    --mobile-type-display: var(--font-size-mobile-display, 2.15rem);
    --mobile-type-metric: var(--font-size-mobile-metric, 1.97rem);
    --mobile-chrome-space-xs: 0.5rem;
    --mobile-chrome-space-sm: 0.75rem;
    --mobile-chrome-space-md: 1rem;
    --mobile-chrome-hit-target: 3.5rem;
    container-type: inline-size;
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: var(--bg-primary);
  }

  .mobile-topbar {
    min-height: calc(var(--mobile-chrome-hit-target) + var(--mobile-chrome-space-xs));
    flex-shrink: 0;
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--mobile-chrome-space-sm);
    padding:
      max(var(--mobile-chrome-space-sm), env(safe-area-inset-top))
      var(--mobile-chrome-space-sm)
      var(--mobile-chrome-space-sm);
    border-bottom: thin solid var(--border-default);
    background: var(--bg-surface);
  }

  .mobile-brand {
    display: inline-flex;
    align-items: center;
    gap: var(--mobile-chrome-space-xs);
    min-width: 0;
  }

  .mobile-app-icon {
    display: block;
    width: 1.45rem;
    height: 1.45rem;
    flex: 0 0 auto;
  }

  .mobile-title {
    color: var(--text-primary);
    font-size: var(--font-size-mobile-body);
    font-weight: 700;
    letter-spacing: -0.01em;
  }

  .mobile-desktop-link {
    width: var(--mobile-chrome-hit-target);
    min-width: var(--mobile-chrome-hit-target);
    min-height: var(--mobile-chrome-hit-target);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0;
    border: thin solid var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    background: var(--bg-surface);
  }

  .mobile-tabs {
    min-width: 0;
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--mobile-chrome-space-xs);
    padding: 0.16rem;
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
  }

  .mobile-tabs a {
    min-height: calc(var(--mobile-chrome-hit-target) - 0.45rem);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: calc(var(--radius-md) - 0.16rem);
    color: var(--text-secondary);
    font-size: var(--font-size-mobile-body);
    font-weight: 650;
    text-decoration: none;
  }

  .mobile-tabs a.mobile-tab--active {
    color: var(--text-primary);
    background: var(--bg-surface);
    box-shadow: var(--shadow-sm);
  }

  .mobile-main {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .mobile-main :global(.controls-bar) {
    align-items: stretch;
    flex-wrap: wrap;
    gap: var(--mobile-chrome-space-sm);
    padding: var(--mobile-chrome-space-sm);
  }

  .mobile-main :global(.search-input) {
    min-height: var(--mobile-chrome-hit-target);
  }

  .focus-layout {
    flex: 1;
    overflow-y: auto;
    background: var(--bg-primary);
    display: flex;
    flex-direction: column;
  }

  .focus-layout--phone {
    --mobile-type-xs: var(--font-size-mobile-xs, 1.08rem);
    --mobile-type-sm: var(--font-size-mobile-sm, 1.17rem);
    --mobile-type-body: var(--font-size-mobile-body, 1.24rem);
    --mobile-type-title: var(--font-size-mobile-title, 1.54rem);
    --mobile-type-display: var(--font-size-mobile-display, 2.15rem);
    --mobile-type-metric: var(--font-size-mobile-metric, 1.97rem);
    --focus-detail-type-xs: var(--mobile-type-xs);
    --focus-detail-type-sm: var(--mobile-type-sm);
    --focus-detail-type-body: var(--mobile-type-body);
    --focus-detail-type-title: var(--mobile-type-title);
    --focus-detail-space-xs: 0.46rem;
    --focus-detail-space-sm: 0.7rem;
    --focus-detail-space-md: 0.9rem;
    --focus-detail-hit-target: 3.75rem;
    --detail-mobile-type-xs: var(--focus-detail-type-xs);
    --detail-mobile-type-sm: var(--focus-detail-type-sm);
    --detail-mobile-type-body: var(--focus-detail-type-body);
    --detail-mobile-type-title: var(--focus-detail-type-title);
    --detail-mobile-hit-target: var(--focus-detail-hit-target);
    overflow: hidden;
    min-width: 0;
  }

  .focus-layout--phone :global(.list-layout),
  .focus-layout--phone :global(.main-area) {
    width: 100%;
    min-width: 0;
  }

  .focus-layout--phone :global(.main-area) {
    overflow-y: auto;
  }

  .focus-layout--phone :global(.pull-detail),
  .focus-layout--phone :global(.issue-detail) {
    box-sizing: border-box;
    width: 100%;
    max-width: none;
    padding: var(--focus-detail-space-sm);
    font-size: var(--font-size-mobile-body);
    line-height: 1.58;
  }

  .focus-layout--phone :global(.pull-detail-content),
  .focus-layout--phone :global(.issue-detail-content) {
    width: 100%;
    max-width: none;
    margin: 0;
    gap: var(--focus-detail-space-md);
  }

  .focus-layout--phone :global(.detail-header),
  .focus-layout--phone :global(.title-line) {
    align-items: flex-start;
    gap: var(--focus-detail-space-sm);
  }

  .focus-layout--phone :global(.detail-title) {
    font-size: var(--font-size-mobile-title);
    line-height: 1.22;
    letter-spacing: -0.015em;
  }

  .focus-layout--phone :global(.meta-row),
  .focus-layout--phone :global(.chips-row),
  .focus-layout--phone :global(.actions-row) {
    gap: var(--focus-detail-space-xs);
  }

  .focus-layout--phone :global(.meta-row) {
    align-items: flex-start;
  }

  .focus-layout--phone :global(.meta-branch) {
    display: inline-flex;
    flex: 1 1 100%;
    min-width: 0;
    max-width: 100%;
    flex-wrap: wrap;
    overflow-wrap: anywhere;
    white-space: normal;
  }

  .focus-layout--phone :global(.branch-name-btn) {
    max-width: 100%;
    white-space: normal;
    overflow-wrap: anywhere;
    word-break: break-word;
    text-align: left;
  }

  .focus-layout--phone :global(.meta-item),
  .focus-layout--phone :global(.meta-sep),
  .focus-layout--phone :global(.sync-indicator),
  .focus-layout--phone :global(.section-title),
  .focus-layout--phone :global(.section-title-inline),
  .focus-layout--phone :global(.loading-placeholder),
  .focus-layout--phone :global(.detail-tab) {
    font-size: var(--font-size-mobile-sm);
    line-height: 1.35;
  }

  .focus-layout--phone :global(.inset-box),
  .focus-layout--phone :global(.markdown-body),
  .focus-layout--phone :global(.comment-editor-input),
  .focus-layout--phone :global(.body-edit-textarea),
  .focus-layout--phone :global(.title-edit-input),
  .focus-layout--phone :global(.add-description-btn),
  .focus-layout--phone :global(.detail-load-error) {
    font-size: var(--font-size-mobile-body);
    line-height: 1.58;
  }

  .focus-layout--phone :global(.inset-box) {
    box-sizing: border-box;
    width: 100%;
    padding: var(--focus-detail-space-sm);
    border-radius: 0.85rem;
  }

  .focus-layout--phone :global(.markdown-body pre),
  .focus-layout--phone :global(.markdown-body code) {
    max-width: 100%;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    word-break: break-word;
  }

  .focus-layout--phone :global(.star-btn),
  .focus-layout--phone :global(.gh-link),
  .focus-layout--phone :global(.copy-icon-btn),
  .focus-layout--phone :global(.copy-number-btn),
  .focus-layout--phone :global(.action-button),
  .focus-layout--phone :global(.detail-tab),
  .focus-layout--phone :global(.add-description-btn) {
    min-width: var(--focus-detail-hit-target);
    min-height: var(--focus-detail-hit-target);
    font-size: var(--font-size-mobile-sm);
  }

  .focus-layout--phone :global(.actions-row) {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .focus-layout--phone :global(.actions-row .action-button) {
    width: 100%;
    justify-content: center;
  }

  .focus-layout--phone :global(.comment-editor-input) {
    min-height: 7.5rem;
    max-height: 45svh;
    padding: var(--focus-detail-space-sm);
    border-radius: 0.85rem;
  }

  .app-main {
    flex: 1;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    position: relative;
  }

  .loading-state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--mobile-chrome-space-sm, 0.5rem);
    flex: 1;
    color: var(--text-muted);
    font-size: var(--font-size-mobile-sm);
    animation: fade-in 0.3s ease;
  }

  :global(.loading-spinner) {
    animation: spin 0.8s linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  @keyframes fade-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
</style>
