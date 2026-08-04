<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import type { Attachment } from "svelte/attachments";
  import {
    Provider,
    PRListView,
    IssueListView,
    ActivityFeedView,
    MobileActivityView,
    ReviewsView,
    FocusListView,
    normalizeRepoFilterSelection,
  } from "@kenn-forge/ui";
  import type { StoreInstances } from "@kenn-forge/ui";
  import type { ActivityItem, ModeVisibility } from "@kenn-forge/ui/api/types";
  import {
    buildFocusPullRequestFilesRoute,
    buildFocusPullRequestRoute,
    buildRepoBrowserRoute,
    buildRoutedItemRoute,
    type PullRequestRouteRef,
    type RepoBrowserRouteRef,
    type RoutedItemRef,
  } from "@kenn-forge/ui/routes";
  import { client } from "./lib/api/runtime.js";

  import AppHeader from "./lib/components/layout/AppHeader.svelte";
  import StatusBar from "./lib/components/layout/StatusBar.svelte";
  import Palette from "./lib/components/keyboard/Palette.svelte";
  import Cheatsheet from "./lib/components/keyboard/Cheatsheet.svelte";
  import NewWorkspaceDialog from "./lib/components/terminal/NewWorkspaceDialog.svelte";
  import {
    closeNewWorkspaceDialog,
    getNewWorkspaceSeedRepo,
    isNewWorkspaceDialogOpen,
  } from "./lib/stores/new-workspace.svelte.js";
  import RepoSummaryPage from "./lib/components/repositories/RepoSummaryPage.svelte";
  import SettingsPage from "./lib/components/settings/SettingsPage.svelte";
  import WorkspaceHost from "./lib/components/terminal/WorkspaceHost.svelte";
  import WorkspacePaneControls from "./lib/components/terminal/WorkspacePaneControls.svelte";
  import WorkspaceEmbedShell from "./lib/components/terminal/WorkspaceEmbedShell.svelte";
  import WorkspaceFirstRunPanel from "./lib/components/terminal/WorkspaceFirstRunPanel.svelte";
  import DesignSystemPage from "./lib/components/design-system/DesignSystemPage.svelte";
  import OnboardingFlow from "./lib/components/onboarding/OnboardingFlow.svelte";
  import KataFeature from "./lib/features/kata/KataFeature.svelte";
  import RepoBrowserFeature from "./lib/features/repo-browser/RepoBrowserFeature.svelte";
  import { fetchKataDaemons } from "./lib/api/kata/daemons.js";
  import { searchKataTaskReferences } from "./lib/api/kata/snapshot.js";
  import { createKataTaskAPI } from "./lib/api/kata/taskClient.js";
  import type { KataIssueNavigationTarget } from "./lib/api/kata/navigation.js";
  import type { KataTaskViewName } from "./lib/api/kata/taskTypes.js";
  import { createDocsAPI } from "./lib/api/docs/api.js";
  import { createKataAuxiliaryAuthority } from "./lib/features/kata/kataAuxiliaryAuthority.svelte.js";
  import { FlashBanner, Spinner } from "@kenn-io/kit-ui";
  import { MonitorIcon } from "./lib/icons.ts";
  import { showFlash } from "@kenn-forge/ui/stores/flash";
  import { initItemRefHandler } from "./lib/utils/itemRefHandler.js";
  import { globalRepoForSelectedRoute } from "./lib/utils/repoSelectionSync.js";
  import { runAppStartup } from "./lib/utils/appStartup.js";
  import { waitUntilBackendReady } from "./lib/utils/backendReadiness.js";
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
    getActiveKataDaemon,
    getKataDaemonRoster,
    getKataDaemonRosterLoaded,
    setActiveKataDaemon,
    setKataDaemonRoster,
  } from "./lib/stores/active-kata-daemon.svelte.js";
  import {
    initContainerObserver,
    isNarrow,
  } from "./lib/stores/container.svelte.js";
  import {
    getRoute,
    getPage,
    navigate,
    replaceUrl,
    getBasePath,
    isMobilePage,
    getDetailTab,
    getSelectedPRFromRoute,
    type RoutableItemRef,
  } from "./lib/stores/router.svelte.ts";
  import { getInlineWorkspaceController, tabSlotAttachment } from "./lib/stores/workspace-host.svelte.ts";
  import {
    buildActivitySelectionSearch,
    parseActivitySelection,
    type ActivityDetailTab,
  } from "./lib/utils/activitySelection.js";
  import { docsHref } from "./lib/api/docs/route.js";
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
  import {
    readOnboardingState,
    shouldStartOnboarding,
    writeOnboardingState,
    type OnboardingState,
  } from "./lib/onboarding/onboarding-state.js";
  import { registerScopedActions } from "./lib/stores/keyboard/registry.svelte.js";
  import {
    defaultActions,
    setStoreInstances,
  } from "./lib/stores/keyboard/actions.js";
  import { dispatchKeydown } from "./lib/stores/keyboard/dispatch.svelte.js";
  import { buildContext } from "./lib/stores/keyboard/context.svelte.js";
  import { searchModePalette } from "./lib/stores/keyboard/mode-palette-search.js";
  import { registerPRDetailActions } from "./lib/stores/keyboard/pr-detail-actions.js";
  import type { PRDetailActionInput } from "../../packages/ui/src/components/detail/keyboard-actions.js";
  import type { Context } from "./lib/stores/keyboard/types.js";

  type DocsRouteState = {
    mode: "docs";
    folder: string | null;
    doc: string | null;
  };

  type DocsFeatureComponent = typeof import("./lib/features/docs/DocsFeature.svelte").default;

  let stores = $state<StoreInstances | undefined>();
  let appReady = $state(false);
  let viewportWidth = $state(window.innerWidth);
  let renderedHeaderHeight = $state(0);
  let renderedMobileHeaderHeight = $state(0);
  let hasCoarsePointer = $state(window.matchMedia("(pointer: coarse)").matches);
  let kataDefaultDaemonId = $state<string | undefined>(undefined);
  let DocsFeature = $state<DocsFeatureComponent | null>(null);
  let docsLoading = $state(false);
  let docsLoadError = $state<string | null>(null);
  let docsRetryFailures = 0;
  let onboardingState = $state<OnboardingState | null>(readOnboardingState());
  let docsRoute = $state<DocsRouteState>({
    mode: "docs",
    folder: null,
    doc: null,
  });
  let cleanupFullAppShell: (() => void) | undefined;
  let fullShellStores: StoreInstances | undefined;
  const appIconSrc = `${getBasePath().replace(/\/$/, "")}/favicon.svg`;
  const kataAPI = createKataTaskAPI();
  const kataWorkspaceAPI = createKataTaskAPI();
  const kataAuxiliaryAuthority = createKataAuxiliaryAuthority();

  const trackMobileHeaderHeight: Attachment<HTMLElement> = (node) => {
    const update = () => {
      renderedMobileHeaderHeight = node.getBoundingClientRect().height;
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(node);
    return () => {
      observer.disconnect();
      renderedMobileHeaderHeight = 0;
    };
  };
  const docsAPI = createDocsAPI();
  const onboardingActive = $derived(
    appReady
      && stores?.settings.isSettingsLoaded() === true
      && shouldStartOnboarding(
        getPage(),
        stores.settings.hasConfiguredRepos(),
        onboardingState,
      ),
  );

  function startOnboarding(): void {
    if (onboardingState === "active") return;
    onboardingState = "active";
    writeOnboardingState("active");
  }

  function dismissOnboarding(): void {
    onboardingState = "dismissed";
    writeOnboardingState("dismissed");
  }

  function completeOnboarding(): void {
    onboardingState = "complete";
    writeOnboardingState("complete");
  }

  function stopFullAppShell() {
    fullShellStores?.events.disconnect();
    // runAppStartup begins sync polling once the shell is ready; its cancel
    // only aborts in-flight startup, so the interval must be stopped here or
    // it keeps firing after teardown (leaking across embed-route navigation
    // and, in jsdom tests, hitting whichever fetch stub is current).
    fullShellStores?.sync.stopPolling();
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

  type KataRouteUpdate = {
    issue?: string | null;
    view?: KataTaskViewName | null;
    scope?: string | null;
    daemon?: string | null;
  };

  function currentKataRouteUpdate(): KataRouteUpdate {
    const route = getRoute();
    if (route.page !== "kata") return {};
    return {
      issue: route.issue ?? null,
      view: route.view ?? null,
      scope: route.scope ?? null,
      daemon: route.daemon ?? null,
    };
  }

  function kataHref(update: KataRouteUpdate = {}): string {
    const params = new URLSearchParams();
    if (update.view) params.set("view", update.view);
    if (update.scope) params.set("scope", update.scope);
    if (update.issue) params.set("issue", update.issue);
    if (update.daemon) params.set("daemon", update.daemon);
    const query = params.toString();
    return query ? `/kata?${query}` : "/kata";
  }

  function kataIssueHref(target: KataIssueNavigationTarget, daemon?: string): string {
    return kataHref({
      view: target.status === "closed" ? "logbook" : "all",
      scope: target.project_uid ?? null,
      issue: target.uid,
      daemon: daemon ?? null,
    });
  }

  function selectKataIssue(uid: string | null): void {
    if (uid === null) {
      navigate("/kata");
      return;
    }
    navigate(kataHref({ ...currentKataRouteUpdate(), issue: uid }));
  }

  function openKataIssue(target: KataIssueNavigationTarget): void {
    const targetDaemon = target.daemon_id && getKataDaemonRoster().includes(target.daemon_id)
      ? target.daemon_id
      : undefined;
    navigate(kataIssueHref(target, targetDaemon));
  }

  let auxiliaryNavigationToken = 0;

  async function openAuxiliaryKataIssue(uid: string, daemonID?: string): Promise<void> {
    // UID-only sources carry no status/project authority, and rows in the
    // shared auxiliary snapshot can be stale during invalidation reloads.
    // Always resolve routing authority through an isolated pinned selection,
    // honoring an explicit daemon (for example a daemon-bound Docs folder).
    // Only the latest click may navigate or surface an error: an older
    // selection resolving after a newer one must not steal the route.
    const token = ++auxiliaryNavigationToken;
    try {
      const selection = await kataAuxiliaryAuthority.selectIssue(uid, daemonID);
      if (token !== auxiliaryNavigationToken) return;
      navigate(kataIssueHref(
        {
          uid,
          status: selection.detail.issue.status,
          project_uid: selection.detail.issue.project_uid,
        },
        selection.daemonID,
      ));
    } catch {
      if (token !== auxiliaryNavigationToken) return;
      showFlash("Could not open linked task", { tone: "danger" });
    }
  }

  function updateKataRoute(update: KataRouteUpdate, options?: { replace?: boolean }): void {
    const href = kataHref({ ...currentKataRouteUpdate(), ...update });
    if (options?.replace) replaceUrl(href);
    else navigate(href);
  }

  function updateRepoBrowserRoute(route: RepoBrowserRouteRef, options?: { replace?: boolean }): void {
    const href = buildRepoBrowserRoute(route);
    if (options?.replace) replaceUrl(href);
    else navigate(href);
  }

  async function importDocsFeature(retryAttempt: number): Promise<typeof DocsFeature> {
    if (retryAttempt > 1) return (await import("./lib/features/docs/DocsFeature.svelte?retry2")).default;
    if (retryAttempt > 0) return (await import("./lib/features/docs/DocsFeature.svelte?retry")).default;
    return (await import("./lib/features/docs/DocsFeature.svelte")).default;
  }

  function featureLoadMessage(error: unknown): string {
    if (error instanceof Error && error.message) return error.message;
    return "Could not load feature";
  }

  function reloadAfterLazyFeatureRetryFailure(): void {
    const url = new URL(window.location.href);
    url.searchParams.set("_forge_feature_retry", String(Date.now()));
    window.location.assign(url.toString());
  }

  async function loadDocsFeature(options: { retry?: boolean } = {}): Promise<void> {
    if (DocsFeature || docsLoading || (docsLoadError && !options.retry)) return;
    if (options.retry && docsRetryFailures >= 2) {
      reloadAfterLazyFeatureRetryFailure();
      return;
    }
    docsLoading = true;
    docsLoadError = null;
    try {
      DocsFeature = await importDocsFeature(options.retry ? docsRetryFailures + 1 : 0);
      docsRetryFailures = 0;
    } catch (error) {
      docsLoadError = featureLoadMessage(error);
      if (options.retry) {
        docsRetryFailures += 1;
      }
    } finally {
      docsLoading = false;
    }
  }

  function openDoc(folder: string, relPath: string): void {
    navigate(docsHref({ mode: "docs", folder, doc: relPath }));
  }

  async function runModePaletteSearch(query: string) {
    return searchModePalette(query, { kata: kataAuxiliaryAuthority, docs: docsAPI });
  }

  async function loadKataDaemonRoster(): Promise<{
    daemons: Awaited<ReturnType<typeof fetchKataDaemons>>;
    ids: string[];
    defaultId: string | undefined;
  }> {
    const daemons = await fetchKataDaemons();
    return {
      daemons,
      ids: daemons.map((daemon) => daemon.id),
      defaultId: daemons.find((daemon) => daemon.default)?.id,
    };
  }

  function loadKataDaemonRosterAfterBackendReady(signal: AbortSignal): void {
    void (async () => {
      try {
        const roster = await loadKataDaemonRoster();
        if (signal.aborted) return;
        setKataDaemonRoster(roster.ids, roster.defaultId);
        kataDefaultDaemonId = roster.defaultId;
      } catch {
        if (signal.aborted) return;
        kataDefaultDaemonId = undefined;
        setKataDaemonRoster([], undefined);
      }
    })();
  }

  function isModeVisible(mode: keyof ModeVisibility): boolean {
    return stores?.settings.isModeVisible(mode) ?? true;
  }

  async function openKataShortId(shortId: string, project?: string, daemonId?: string): Promise<void> {
    try {
      const requestedReference = project ? `${project}#${shortId}` : shortId;
      const results = await searchKataTaskReferences(
        requestedReference,
        { ...(daemonId ? { daemon_id: daemonId } : {}), status: "all" },
      );
      const match = (results.references ?? []).find((reference) =>
        reference.reference === requestedReference
        || reference.qualified_id === requestedReference
        || (project !== undefined
          && reference.short_id === shortId
          && (reference.project_name === project || reference.project_uid === project)),
      );
      if (match) {
        openKataIssue({
          uid: match.uid,
          status: match.status,
          project_uid: match.project_uid,
          ...(daemonId ? { daemon_id: daemonId } : {}),
        });
      }
    } catch {
      showFlash("Could not open linked task", { tone: "danger" });
    }
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
      waitUntilBackendReady,
      afterBackendReady: loadKataDaemonRosterAfterBackendReady,
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

  $effect(() => {
    const route = getRoute();
    if (route.page !== "docs") return;
    docsRoute = {
      mode: "docs",
      folder: route.folder,
      doc: route.doc,
    };
    void loadDocsFeature();
  });

  $effect(() => {
    if (!appReady || !getKataDaemonRosterLoaded()) return;
    const daemonID = getActiveKataDaemon() ?? kataDefaultDaemonId;
    const load = untrack(() => kataAuxiliaryAuthority.load(daemonID));
    void load.catch(() => {});
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
      window.__KENN_FORGE_FORCE_MOBILE_ROUTES__ === true ||
      import.meta.env.VITE_KENN_FORGE_FORCE_MOBILE_ROUTES === "1" ||
      import.meta.env.VITE_KENN_FORGE_FORCE_MOBILE_ROUTES === "true"
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

  // Phone presentation gets the kit-ui touch type scale even when the
  // pointer is not coarse (forced-mobile flags, UA-only detection, browser
  // engines that don't emulate pointer media). Real handheld devices match
  // the (hover: none) and (pointer: coarse) query in theme.css regardless
  // of this class.
  const usesPhoneTypeScale = $derived(
    isMobilePage(getPage())
      || shouldUseResponsiveMobileActivityPresentation()
      || (shouldUseFocusPresentation() && useFocusLayoutClass()),
  );

  $effect(() => {
    document.documentElement.classList.toggle("kit-type-touch", usesPhoneTypeScale);
    return () => {
      document.documentElement.classList.remove("kit-type-touch");
    };
  });

  function shouldUseResponsiveMobileActivityPresentation(): boolean {
    if (shouldUseDesktopOnPhone()) return false;
    if (getPage() !== "activity") return false;
    return isCompactViewport() || shouldForceMobileRoutes();
  }

  function flashTopOffset(): string {
    if (onboardingActive) return "0";
    if (shouldUseFocusPresentation() || isHeaderHidden()) return "0";
    if (isMobilePage(getPage()) || shouldUseResponsiveMobileActivityPresentation()) {
      return renderedMobileHeaderHeight > 0 ? `${renderedMobileHeaderHeight}px` : "0";
    }
    return renderedHeaderHeight > 0 ? `${renderedHeaderHeight}px` : "var(--header-height)";
  }

  function navigateFocusPRDetailTab(
    ref: Parameters<typeof buildFocusPullRequestRoute>[0],
    tab: "conversation" | "files",
    options?: { replace?: boolean },
  ): void {
    const path =
      tab === "files" ? buildFocusPullRequestFilesRoute(ref) : buildFocusPullRequestRoute(ref);
    // Replace when the view says this only records which of two simultaneously
    // visible panes the user is in, so moving between them does not fill the
    // Back stack.
    if (options?.replace) replaceUrl(path);
    else navigate(path);
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

  function getNormalizedGlobalRepo(repo: string | undefined = getGlobalRepo()): string | undefined {
    return normalizeRepoFilterSelection(
      repo,
      (stores?.settings.getConfiguredRepos?.() ?? []).map((configuredRepo) => ({
        provider: configuredRepo.provider,
        platformHost: configuredRepo.platform_host,
        repoPath: configuredRepo.repo_path,
        isGlob: configuredRepo.is_glob,
      })),
    );
  }

  onDestroy(() => {
    kataAuxiliaryAuthority.stop();
    stopFullAppShell();
    stores?.events.disconnect();
  });

  $effect(() => {
    const repo = getNormalizedGlobalRepo();
    if (repo !== getGlobalRepo()) {
      setGlobalRepo(repo);
      return;
    }
    if (!appReady || !stores) {
      lastRepo = repo;
      return;
    }
    if (repo === lastRepo) return;
    lastRepo = repo;
    void stores.pulls.loadPulls();
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
        // kit-ui-check-ignore: string action id, not drawer markup
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
  // imported from inside @kenn-forge/ui. The buildPRDetailInput closure
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
    // Merge remains tied to the verified reviewed_head_sha, but approval
    // should include the latest synced provider head when one is known.
    const renderedHeadSha = detail.reviewed_head_sha ?? "";
    const latestPlatformHeadSha = detail.platform_head_sha ?? "";
    // TS cannot carry the !stores narrowing into the closure below.
    const appStores = stores;
    // Per-operation availability from the detail payload (rate limits,
    // missing write credential on GitHub App split hosts). An absent
    // entry gates nothing so older payloads keep working.
    const operations = detail.repo?.operations;
    const operationAvailable = (
      op: { available?: boolean } | undefined,
    ): boolean => op === undefined || op.available === true;
    return {
      pr: {
        State: pr.State,
        IsDraft: pr.IsDraft,
        MergeableState: pr.MergeableState,
        platform_head_sha: latestPlatformHeadSha,
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
        approve: capabilities.review_mutation
          && operationAvailable(operations?.submit_review),
        merge: capabilities.merge_mutation
          && operationAvailable(operations?.merge_pr),
        markReady: capabilities.ready_for_review
          && operationAvailable(operations?.mark_ready_for_review),
        approveWorkflows: workflowApprovalReady
          && operationAvailable(operations?.approve_workflow),
      },
      // pr.merge is not registered, so repoSettings is not consulted.
      repoSettings: null,
      // Same identity check feeds `stale`; reaching this return means
      // selection and detail agree, so the action is fresh.
      stale: false,
      stores: { pulls: stores.pulls, detail: stores.detail },
      client,
      requireHeadPin: capabilities.mutation_head_binding,
      ...(renderedHeadSha !== "" && { expectedHeadSha: renderedHeadSha }),
      approveCommentBody: "",
      // A head-pin conflict means the rendered head moved or was never
      // synced; reload the detail (a sync-enabled load) so the user
      // re-reviews current state, mirroring PullDetail's own handler.
      // The flash from onError carries the explanation.
      onHeadConflict: () => {
        void appStores.detail.loadDetail(sel.owner, sel.name, sel.number, {
          provider: sel.provider,
          platformHost: sel.platformHost,
          repoPath: sel.repoPath,
        });
      },
      onError: (msg: string) => showFlash(msg, { tone: "danger" }),
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
    onError={(msg) => showFlash(msg, { tone: "danger" })}
    onNotification={showFlash}
    onNavigate={(e, options) => {
      const path = typeof e === "string" ? e : e.path;
      if (options?.replace) replaceUrl(path);
      else navigate(path);
    }}
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
      getGlobalRepo: getNormalizedGlobalRepo,
      getGroupByRepo: () => stores?.grouping.getGroupByRepo() ?? true,
      getActiveWorktreeKey,
    }}
    getActivitySelection={() => drawerItem}
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
  <!-- Mounted once above the focus/full-shell branching so flashes raised
       through the shared store stay visible in every presentation, not just
       the desktop shell. -->
  <FlashBanner top={flashTopOffset()} />
  {#if onboardingActive && stores}
    <OnboardingFlow
      {stores}
      iconSrc={appIconSrc}
      onStart={startOnboarding}
      onDismiss={dismissOnboarding}
      onComplete={completeOnboarding}
    />
  {:else if shouldUseFocusPresentation()}
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
          onDetailTabChange={(tab, options) => navigateFocusPRDetailTab(selectedPR, tab, options)}
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
      <header class="mobile-topbar" {@attach trackMobileHeaderHeight}>
        <span class="mobile-brand">
          <img class="mobile-app-icon" src={appIconSrc} alt="" aria-hidden="true" />
          <span class="mobile-title">kenn-forge</span>
        </span>

        <nav class="mobile-tabs" aria-label="Phone navigation">
          {#if isModeVisible("activity")}
            <a
              class:mobile-tab--active={getPage() === "mobile-activity" || getPage() === "activity"}
              href="/m"
              onclick={(e) => {
                e.preventDefault();
                navigateMobile("/m");
              }}
            >Activity</a>
          {/if}
          {#if isModeVisible("pulls")}
            <a
              class:mobile-tab--active={getPage() === "mobile-pulls"}
              href="/m/pulls"
              onclick={(e) => {
                e.preventDefault();
                navigateMobile("/m/pulls");
              }}
            >PRs</a>
          {/if}
          {#if isModeVisible("issues")}
            <a
              class:mobile-tab--active={getPage() === "mobile-issues"}
              href="/m/issues"
              onclick={(e) => {
                e.preventDefault();
                navigateMobile("/m/issues");
              }}
            >Issues</a>
          {/if}
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
            <Spinner size={18} />
            Loading
          </div>
        {:else if getPage() === "mobile-pulls"}
          <FocusListView listType="mrs" />
        {:else if getPage() === "mobile-issues"}
          <FocusListView listType="issues" />
        {:else}
          <MobileActivityView
            selectedRepo={getNormalizedGlobalRepo()}
            onRepoChange={setGlobalRepo}
            onSelectItem={handleActivitySelect}
          />
        {/if}
      </main>
    </section>
  {:else}
    {#if !isHeaderHidden()}
      <AppHeader onheightchange={(height) => (renderedHeaderHeight = height)} />
    {/if}

    <main class="app-main">
      {#if getPage() === "design-system"}
        <DesignSystemPage />
      {:else if !appReady}
        <div class="loading-state">
          <Spinner size={18} />
          Loading
        </div>
      {:else if getPage() === "settings"}
        <SettingsPage />
      {:else if getPage() === "activity"}
        <!-- Desktop shell only: focus-presentation and mobile branches of
             this view get no controller (structural eligibility). -->
        <ActivityFeedView
          {drawerItem}
          onSelectItem={handleActivitySelect}
          onCloseDrawer={closeDrawer}
          detailTab={drawerItem?.detailTab ?? "conversation"}
          onDetailTabChange={handleActivityDetailTabChange}
          onDrawerItemChange={handleActivityDrawerItemChange}
          inlineWorkspace={getInlineWorkspaceController("activity")}
          {workspacePaneControls}
        />
      {:else if getPage() === "repos"}
        <RepoSummaryPage />
      {:else if getPage() === "repo-browser"}
        {@const route = getRoute()}
        {#if route.page === "repo-browser"}
          <RepoBrowserFeature
            {client}
            {route}
            onRouteChange={updateRepoBrowserRoute}
          />
        {/if}
      {:else if getPage() === "kata"}
        {@const route = getRoute()}
        {#if route.page === "kata"}
          <KataFeature
            api={kataWorkspaceAPI}
            searchReferences={searchKataTaskReferences}
            selectedIssueUID={route.issue ?? null}
            routeViewName={route.view ?? null}
            routeScopeUID={route.scope ?? null}
            requestedDaemonId={route.daemon ?? null}
            onSelectedIssueChange={selectKataIssue}
            onRouteStateChange={updateKataRoute}
          />
        {/if}
      {:else if getPage() === "docs"}
        {#if docsLoadError}
          <div class="loading-state">
            <span>{docsLoadError}</span>
            <button type="button" onclick={() => void loadDocsFeature({ retry: true })}>Retry loading Docs</button>
          </div>
        {:else if !DocsFeature}
          <div class="loading-state">
            <Spinner size={18} />
            Loading Docs
          </div>
        {/if}
      {:else if getPage() === "pulls"}
        {@const selectedPR =
          getSelectedPRFromRoute() ??
          stores?.pulls.getSelectedPR() ??
          null}
        {@const detailTab = getDetailTab()}
        <!-- Desktop shell only: focus-presentation and mobile branches of
             this view get no controller (structural eligibility). -->
        <PRListView
          {selectedPR}
          {detailTab}
          isSidebarCollapsed={isSidebarCollapsed()}
          sidebarWidth={getSidebarWidth()}
          sidebarOverlay={isNarrow()}
          onSidebarResize={handleSidebarResize}
          inlineWorkspace={getInlineWorkspaceController("prs")}
          {workspacePaneControls}
        />
      {:else if getPage() === "issues"}
        {@const selectedIssue =
          stores?.issues.getSelectedIssue() ?? null}
          <!-- Desktop shell only: focus-presentation and mobile branches of
               this view get no controller (structural eligibility). -->
          <IssueListView
            {selectedIssue}
          isSidebarCollapsed={isSidebarCollapsed()}
          sidebarWidth={getSidebarWidth()}
          sidebarOverlay={isNarrow()}
          onSidebarResize={handleSidebarResize}
          inlineWorkspace={getInlineWorkspaceController("issues")}
          {workspacePaneControls}
        />
      {:else if getPage() === "reviews"}
        {@const route = getRoute()}
        {#if route.page === "reviews" && route.jobId != null}
          <ReviewsView jobId={route.jobId} />
        {:else}
          <ReviewsView />
        {/if}
      {:else if getPage() === "project-intake"}
        {@const route = getRoute()}
        {#if route.page === "project-intake"}
          <WorkspaceFirstRunPanel
            firstRun={false}
            hostKey={route.hostKey}
          />
        {/if}
      {:else if getPage() === "workspaces" || getPage() === "terminal"}
        <!-- Slot for the App-owned WorkspaceHost (mounted below, outside
             this page-branch chain) to reparent its live WTV wrapper into.
             WorkspaceHost reacts to workspaceId/route changes internally so
             the page doesn't flash on navigation. -->
        <div class="workspace-tab-slot" {@attach tabSlotAttachment}></div>
      {/if}

      {#if appReady && DocsFeature}
        <section
          class="feature-shell docs-shell"
          hidden={getPage() !== "docs"}
          aria-hidden={getPage() !== "docs"}
        >
          <DocsFeature
            route={docsRoute}
            onRouteChange={(next, options) => {
              if (options?.replace) replaceUrl(docsHref(next));
              else navigate(docsHref(next));
            }}
            searchReferences={searchKataTaskReferences}
            onOpenIssue={openAuxiliaryKataIssue}
            onOpenKataShortId={(shortId, project, daemonId) => {
              void openKataShortId(shortId, project, daemonId);
            }}
          />
        </section>
      {/if}

      <!-- Mounted once across every desktop-shell page (this element is
           never torn down by the page-branch {#if} chain above) so the
           live WTV wrapper it hosts survives page switches; it physically
           reparents into the tab slot, an inline dock slot, or a hidden
           parking node depending on the route and inline claims. Gated on
           appReady like the pages: a direct /terminal/{id} load must not
           start workspace/runtime requests before the backend readiness
           poll and settings initialization complete — early failures
           would sit in the error state until a manual retry.
           appReady only resets when the whole shell tears down, so this
           still never remounts across page switches. -->
      {#if appReady}
        <WorkspaceHost
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

    {#if !onboardingActive}
      <Palette
        modeSearch={runModePaletteSearch}
        onOpenKataIssue={openKataIssue}
        onOpenDoc={openDoc}
      />
      <Cheatsheet />
      <NewWorkspaceDialog
        open={isNewWorkspaceDialogOpen()}
        seedRepo={getNewWorkspaceSeedRepo()}
        onClose={closeNewWorkspaceDialog}
      />
    {/if}
  </Provider>
{/if}

<!-- Handed to every detail view: the controls themselves come from the hosted
     workspace's live view, and this component is the popover that holds them in a
     pane's tab strip. Declared here because packages/ui cannot import it. -->
{#snippet workspacePaneControls(showStripActions: boolean)}
  <WorkspacePaneControls {showStripActions} />
{/snippet}

<style>
  .mobile-shell {
    --mobile-type-xs: var(--font-size-xs);
    --mobile-type-sm: var(--font-size-sm);
    --mobile-type-body: var(--font-size-md);
    --mobile-type-title: var(--font-size-xl);
    --mobile-type-display: var(--font-size-2xl);
    --mobile-type-metric: var(--font-size-2xl);
    --mobile-chrome-space-xs: 6.5px;
    --mobile-chrome-space-sm: 10px;
    --mobile-chrome-space-md: 13px;
    --mobile-chrome-hit-target: 45.5px;
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
    width: 19px;
    height: 19px;
    flex: 0 0 auto;
  }

  .mobile-title {
    color: var(--text-primary);
    font-size: var(--font-size-md);
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
    padding: 2px;
    border: thin solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-inset);
  }

  .mobile-tabs a {
    min-height: calc(var(--mobile-chrome-hit-target) - 6px);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: calc(var(--radius-md) - 2px);
    color: var(--text-secondary);
    font-size: var(--font-size-md);
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

  .mobile-main :global(.kit-search-input) {
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
    --mobile-type-xs: var(--font-size-xs);
    --mobile-type-sm: var(--font-size-sm);
    --mobile-type-body: var(--font-size-md);
    --mobile-type-title: var(--font-size-xl);
    --mobile-type-display: var(--font-size-2xl);
    --mobile-type-metric: var(--font-size-2xl);
    --focus-detail-type-xs: var(--mobile-type-xs);
    --focus-detail-type-sm: var(--mobile-type-sm);
    --focus-detail-type-body: var(--mobile-type-body);
    --focus-detail-type-title: var(--mobile-type-title);
    --focus-detail-space-xs: 6px;
    --focus-detail-space-sm: 9px;
    --focus-detail-space-md: 12px;
    --focus-detail-hit-target: 49px;
    --detail-mobile-type-xs: var(--focus-detail-type-xs);
    --detail-mobile-type-sm: var(--focus-detail-type-sm);
    --detail-mobile-type-body: var(--focus-detail-type-body);
    --detail-mobile-type-title: var(--focus-detail-type-title);
    --detail-mobile-hit-target: var(--focus-detail-hit-target);
    overflow: hidden;
    min-width: 0;
  }

  .focus-layout--phone :global(.kit-sidebar-layout),
  .focus-layout--phone :global(.kit-sidebar-layout__main) {
    width: 100%;
    min-width: 0;
  }

  .focus-layout--phone :global(.kit-sidebar-layout__main) {
    overflow-y: auto;
  }

  .focus-layout--phone :global(.pull-detail),
  .focus-layout--phone :global(.issue-detail) {
    box-sizing: border-box;
    width: 100%;
    max-width: none;
    padding: var(--focus-detail-space-sm);
    font-size: var(--font-size-md);
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
    font-size: var(--font-size-xl);
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
    font-size: var(--font-size-sm);
    line-height: 1.35;
  }

  .focus-layout--phone :global(.inset-box),
  .focus-layout--phone :global(.markdown-body),
  .focus-layout--phone :global(.comment-editor-input),
  .focus-layout--phone :global(.body-edit-textarea),
  .focus-layout--phone :global(.title-edit-input),
  .focus-layout--phone :global(.add-description-btn),
  .focus-layout--phone :global(.detail-load-error) {
    font-size: var(--font-size-md);
    line-height: 1.58;
  }

  .focus-layout--phone :global(.inset-box) {
    box-sizing: border-box;
    width: 100%;
    padding: var(--focus-detail-space-sm);
    border-radius: 11px;
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
  .focus-layout--phone :global(.pull-detail-content .meta-row .copy-number-btn),
  .focus-layout--phone :global(.detail-description__toggle),
  .focus-layout--phone :global(.kit-button),
  .focus-layout--phone :global(.detail-tab),
  .focus-layout--phone :global(.add-description-btn) {
    min-width: var(--focus-detail-hit-target);
    min-height: var(--focus-detail-hit-target);
    font-size: var(--font-size-sm);
  }

  .focus-layout--phone :global(.actions-row) {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .focus-layout--phone :global(.actions-row .kit-button) {
    width: 100%;
    justify-content: center;
  }

  .focus-layout--phone :global(.comment-editor-input) {
    min-height: 97.5px;
    max-height: 45svh;
    padding: var(--focus-detail-space-sm);
    border-radius: 11px;
  }

  .app-main {
    flex: 1;
    min-height: 0;
    overflow: clip;
    display: flex;
    flex-direction: column;
    position: relative;
  }

  .workspace-tab-slot {
    display: flex;
    flex: 1;
    min-height: 0;
    min-width: 0;
  }

  .loading-state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--mobile-chrome-space-sm, 8px);
    flex: 1;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    animation: fade-in 0.3s ease;
  }

  .feature-shell {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .feature-shell[hidden] {
    display: none;
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
