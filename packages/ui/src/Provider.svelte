<script lang="ts">
  import { setContext, onDestroy } from "svelte";
  import {
    API_CLIENT_KEY, ACTIONS_KEY, NAVIGATE_KEY, EVENT_KEY,
    PREPARE_ROUTE_KEY, WORKSPACE_COMMAND_KEY,
    STORES_KEY, UI_CONFIG_KEY, SIDEBAR_KEY, HOST_STATE_KEY,
    ROBOREV_CLIENT_KEY,
  } from "./context.js";
  import { createRoborevClient } from "./api/roborev/client.js";
  import {
    createDaemonStore,
  } from "./stores/roborev/daemon.svelte.js";
  import {
    createJobsStore,
  } from "./stores/roborev/jobs.svelte.js";
  import {
    createReviewStore,
  } from "./stores/roborev/review.svelte.js";
  import {
    createLogStore,
  } from "./stores/roborev/log.svelte.js";
  import type {
    MiddlemanClient, ActionRegistry, NavigateCallback,
    EventCallback, PrepareRouteCallback, WorkspaceCommandCallback,
    HostStateAccessors, StoreInstances, UIConfig,
    SidebarAccessors,
  } from "./types.js";
  import type {
    PullsStoreOptions,
  } from "./stores/pulls.svelte.js";
  import type {
    IssuesStoreOptions,
  } from "./stores/issues.svelte.js";
  import type {
    DetailStoreOptions,
  } from "./stores/detail.svelte.js";
  import type {
    ActivityStoreOptions,
  } from "./stores/activity.svelte.js";
  import type {
    DiffStoreOptions,
  } from "./stores/diff.svelte.js";
  import {
    createPullsStore,
  } from "./stores/pulls.svelte.js";
  import {
    createIssuesStore,
  } from "./stores/issues.svelte.js";
  import {
    createDetailStore,
  } from "./stores/detail.svelte.js";
  import {
    createActivityStore,
  } from "./stores/activity.svelte.js";
  import {
    createSyncStore,
  } from "./stores/sync.svelte.js";
  import {
    createDiffStore,
  } from "./stores/diff.svelte.js";
  import {
    createGroupingStore,
  } from "./stores/grouping.svelte.js";
  import {
    createCollapsedReposStore,
  } from "./stores/collapsedRepos.svelte.js";
  import {
    createSettingsStore,
  } from "./stores/settings.svelte.js";
  import {
    createEventsStore,
  } from "./stores/events.svelte.js";
  import type {
    ActivitySettings,
    ConfigRepo,
    TerminalSettingsInput,
  } from "./api/types.js";

  interface Props {
    client: MiddlemanClient;
    actions?: ActionRegistry;
    onNavigate?: NavigateCallback;
    onEvent?: EventCallback;
    prepareRoute?: PrepareRouteCallback;
    onWorkspaceCommand?: WorkspaceCommandCallback | undefined;
    hostState?: HostStateAccessors;
    config?: UIConfig;
    sidebar?: SidebarAccessors;
    getPage?: () => string;
    roborevBaseUrl?: string;
    onError?: (msg: string) => void;
    stores?: StoreInstances | undefined;
    children?: import("svelte").Snippet;
  }

  let {
    client,
    actions = {},
    onNavigate = () => {},
    onEvent = () => {},
    prepareRoute = undefined,
    onWorkspaceCommand = undefined,
    hostState = {},
    config = {},
    sidebar = {
      isEmbedded: () => false,
      isSidebarToggleEnabled: () => true,
      toggleSidebar: () => {},
    },
    getPage = () => "",
    roborevBaseUrl = undefined,
    onError = undefined,
    stores = $bindable(),
    children,
  }: Props = $props();

  // All initialization is in this function so its
  // parameters are plain values, not reactive proxies.
  // This avoids state_referenced_locally warnings.
  function init(
    cl: MiddlemanClient,
    hs: HostStateAccessors,
    cfg: UIConfig,
    act: ActionRegistry,
    nav: NavigateCallback,
    evt: EventCallback,
    prep: PrepareRouteCallback | undefined,
    workspaceCmd: WorkspaceCommandCallback | undefined,
    sb: SidebarAccessors,
    gp: () => string,
    roborevBase: string | undefined,
    errorCb: ((msg: string) => void) | undefined,
  ): StoreInstances {
    const grouping = createGroupingStore();
    const collapsedRepos = createCollapsedReposStore();
    const settingsStore = createSettingsStore();

    const pullsOpts: PullsStoreOptions = { client: cl };
    if (hs.getGlobalRepo) {
      pullsOpts.getGlobalRepo = hs.getGlobalRepo;
    }
    pullsOpts.getGroupByRepo =
      hs.getGroupByRepo ?? grouping.getGroupByRepo;
    if (hs.getView) {
      pullsOpts.getView = hs.getView;
    }
    const pullsStore = createPullsStore(pullsOpts);

    const syncStore = createSyncStore({ client: cl });

    const detailOpts: DetailStoreOptions = {
      client: cl,
      getPage: gp,
      pulls: {
        loadPulls: (p?: unknown) => pullsStore.loadPulls(
          p as Parameters<typeof pullsStore.loadPulls>[0],
        ),
        optimisticKanbanUpdate:
          pullsStore.optimisticKanbanUpdate,
        getPullKanbanStatus:
          pullsStore.getPullKanbanStatus,
      },
      sync: syncStore,
    };
    const detailStore = createDetailStore(detailOpts);

    const issuesOpts: IssuesStoreOptions = {
      client: cl,
      getPage: gp,
      sync: {
        refreshSyncStatus:
          syncStore.refreshSyncStatus,
      },
    };
    if (hs.getGlobalRepo) {
      issuesOpts.getGlobalRepo = hs.getGlobalRepo;
    }
    issuesOpts.getGroupByRepo =
      hs.getGroupByRepo ?? grouping.getGroupByRepo;
    const issuesStore = createIssuesStore(issuesOpts);

    const activityOpts: ActivityStoreOptions = {
      client: cl,
    };
    if (hs.getGlobalRepo) {
      activityOpts.getGlobalRepo = hs.getGlobalRepo;
    }
    if (cfg.basePath != null) {
      const bp = cfg.basePath;
      activityOpts.getBasePath = () => bp;
    }
    const activityStore =
      createActivityStore(activityOpts);

    const diffOpts: DiffStoreOptions = { client: cl };
    if (cfg.basePath != null) {
      const bp = cfg.basePath;
      diffOpts.getBasePath = () => bp;
    }
    const diffStore = createDiffStore(diffOpts);

    function hydrateSettings(
      repos: ConfigRepo[],
      activity: ActivitySettings,
      terminal: TerminalSettingsInput,
    ): void {
      settingsStore.setConfiguredRepos(repos);
      settingsStore.setTerminalSettings(terminal);
      activityStore.hydrateDefaults(activity);
    }

    async function reloadSettingsAfterConfigChange(): Promise<void> {
      const { data } = await cl.GET("/settings");
      if (!data) return;
      hydrateSettings(
        data.repos ?? [],
        {
          view_mode: data.activity.view_mode === "threaded"
            ? "threaded"
            : "flat",
          time_range: (
            data.activity.time_range === "24h" ||
            data.activity.time_range === "30d" ||
            data.activity.time_range === "90d"
          )
            ? data.activity.time_range
            : "7d",
          hide_closed: data.activity.hide_closed,
          hide_bots: data.activity.hide_bots,
          collapse_threads: data.activity.collapse_threads,
        },
        data.terminal,
      );
    }

    async function handleConfigChanged(
      event: { valid: boolean },
    ): Promise<void> {
      if (!event.valid) return;
      await reloadSettingsAfterConfigChange();
      await Promise.all([
        pullsStore.loadPulls(),
        issuesStore.loadIssues(),
        activityStore.loadActivity(),
      ]);
    }

    const eventsStore = createEventsStore({
      ...(cfg.basePath != null && {
        getBasePath: () => cfg.basePath as string,
      }),
      onDataChanged: () => {
        void pullsStore.loadPulls();
        void issuesStore.loadIssues();
        void activityStore.loadActivity();
      },
      onSyncStatus: (status) => {
        syncStore.setSyncStatus(status);
      },
      onConfigChanged: (event) => {
        void handleConfigChanged(event);
      },
      onReconnectStale: () => {
        // The replay ring rolled past the client's cursor while it
        // was disconnected (long sleep, extended network outage).
        // Refetch view state from scratch instead of relying on the
        // missed broadcasts. sync.refreshSyncStatus() picks up the
        // current daemon state since no sync_status frame will replay.
        void pullsStore.loadPulls();
        void issuesStore.loadIssues();
        void activityStore.loadActivity();
        void syncStore.refreshSyncStatus();
      },
    });

    const si: StoreInstances = {
      pulls: pullsStore,
      issues: issuesStore,
      detail: detailStore,
      activity: activityStore,
      sync: syncStore,
      diff: diffStore,
      grouping,
      collapsedRepos,
      settings: settingsStore,
      events: eventsStore,
    };

    if (roborevBase) {
      const bp = (cfg.basePath ?? "/").replace(/\/$/, "");
      const roborevClient = createRoborevClient(
        bp + roborevBase,
      );

      const jobsOpts: Parameters<typeof createJobsStore>[0] = {
        client: roborevClient,
        navigate: nav,
      };
      if (errorCb) jobsOpts.onError = errorCb;
      const jobsStore = createJobsStore(jobsOpts);
      si.roborevJobs = jobsStore;

      const reviewOpts: Parameters<typeof createReviewStore>[0] = {
        client: roborevClient,
      };
      if (errorCb) reviewOpts.onError = errorCb;
      const reviewStore = createReviewStore(reviewOpts);
      si.roborevReview = reviewStore;

      const logStore = createLogStore({
        client: roborevClient,
        baseUrl: bp + roborevBase,
      });
      si.roborevLog = logStore;

      const daemon = createDaemonStore({
        client: roborevClient,
        middlemanClient: cl,
        onRecover: () => {
          void jobsStore.loadJobs();
          const selectedId =
            reviewStore.getSelectedJobId();
          if (selectedId !== undefined) {
            void reviewStore.loadReview(selectedId);
          }
        },
      });
      si.roborevDaemon = daemon;

      setContext(ROBOREV_CLIENT_KEY, roborevClient);
      daemon.startPolling();
    }

    setContext(API_CLIENT_KEY, cl);
    setContext(ACTIONS_KEY, act);
    setContext(NAVIGATE_KEY, nav);
    setContext(EVENT_KEY, evt);
    setContext(PREPARE_ROUTE_KEY, prep ?? null);
    setContext(WORKSPACE_COMMAND_KEY, workspaceCmd ?? null);
    setContext(STORES_KEY, si);
    setContext(UI_CONFIG_KEY, cfg);
    setContext(SIDEBAR_KEY, sb);
    setContext(HOST_STATE_KEY, hs);

    return si;
  }

  // svelte-ignore state_referenced_locally
  stores = init(
    client, hostState, config, actions,
    onNavigate, onEvent, prepareRoute,
    onWorkspaceCommand,
    sidebar, getPage, roborevBaseUrl, onError,
  );

  onDestroy(() => {
    stores?.roborevDaemon?.stopPolling();
  });
</script>

{#if children}
  {@render children()}
{/if}
