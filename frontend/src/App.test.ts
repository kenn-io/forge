import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
// Compile the root component during collection so Vite transform work is not
// charged against the first lazy-feature test's behavioral timeout.
import "./App.svelte";

const featureImports = vi.hoisted(() => ({
  docs: 0,
  failDocsOnce: false,
}));

const startup = vi.hoisted(() => ({
  autoReady: true,
  readyCallbacks: [] as Array<() => void>,
}));

const kataClients = vi.hoisted(() => ({
  create: vi.fn(() => ({})),
}));

const kataReferences = vi.hoisted(() => ({
  search: vi.fn(),
}));

const kataDaemons = vi.hoisted(() => ({
  rows: [] as Array<{ id: string; url: string; default: boolean; auth: "none"; health: "connected" }>,
}));

const kataAuxiliary = vi.hoisted(() => {
  const instance = {
    issues: [],
    daemonID: "home",
    phase: "accepted" as const,
    error: null,
    load: vi.fn(async () => true),
    retry: vi.fn(async () => true),
    selectIssue: vi.fn(),
    stop: vi.fn(),
  };
  return { instance, create: vi.fn(() => instance) };
});

const modePalette = vi.hoisted(() => ({
  search: vi.fn(async (query: string) => ({
    query,
    tasks: { ok: true as const, rows: [], truncated: false },
    docs: { ok: true as const, rows: [], truncated: false },
  })),
}));

const appSurfaceProps = vi.hoisted(() => ({
  palette: null as Record<string, unknown> | null,
  docs: null as Record<string, unknown> | null,
  provider: null as Record<string, unknown> | null,
}));

vi.mock("@kenn-forge/ui", async () => {
  const ProviderMock = (await import("./lib/testing/AppProviderMock.svelte")).default;
  const Stub = (await import("./lib/testing/AppViewStub.svelte")).default;
  return {
    Provider: (anchor: Parameters<typeof ProviderMock>[0], props: Parameters<typeof ProviderMock>[1]) => {
      appSurfaceProps.provider = props as Record<string, unknown>;
      return ProviderMock(anchor, props);
    },
    WorkspaceCreateSplitButton: Stub,
    PRListView: Stub,
    IssueListView: Stub,
    ActivityFeedView: Stub,
    MobileActivityView: Stub,
    ReviewsView: Stub,
    FocusListView: Stub,
    getStores: () => ({
      settings: {
        getLaunchTargets: () => [],
      },
    }),
    normalizeRepoFilterSelection: (repo: string | undefined) => repo,
  };
});

vi.mock("./lib/components/layout/AppHeader.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/layout/StatusBar.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/keyboard/Palette.svelte", async () => {
  const Stub = (await import("./lib/testing/AppViewStub.svelte")).default;
  return {
    default: (anchor: Parameters<typeof Stub>[0], props: Parameters<typeof Stub>[1]) => {
      appSurfaceProps.palette = props;
      return Stub(anchor, props);
    },
  };
});
vi.mock("./lib/components/keyboard/Cheatsheet.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/repositories/RepoSummaryPage.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/settings/SettingsPage.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/terminal/WorkspaceTerminalView.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/terminal/WorkspaceEmbedShell.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/design-system/DesignSystemPage.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/features/kata/KataFeature.svelte", async () => ({
  default: (await import("./lib/features/kata/KataWorkspaceTestStub.svelte")).default,
}));
vi.mock("./lib/features/docs/DocsFeature.svelte", async () => {
  featureImports.docs += 1;
  if (featureImports.failDocsOnce) {
    featureImports.failDocsOnce = false;
    throw new Error("docs chunk unavailable");
  }
  const Feature = (await import("./lib/testing/AppDocsFeatureMock.svelte")).default;
  return {
    default: (anchor: Parameters<typeof Feature>[0], props: Parameters<typeof Feature>[1]) => {
      appSurfaceProps.docs = props as Record<string, unknown>;
      return Feature(anchor, props);
    },
  };
});
vi.mock("./lib/features/docs/DocsFeature.svelte?retry", async () => {
  featureImports.docs += 1;
  return {
    default: (await import("./lib/testing/AppDocsFeatureMock.svelte")).default,
  };
});
vi.mock("./lib/features/docs/DocsFeature.svelte?retry2", async () => {
  featureImports.docs += 1;
  return {
    default: (await import("./lib/testing/AppDocsFeatureMock.svelte")).default,
  };
});
vi.mock("./lib/api/kata/daemons.js", () => ({
  fetchKataDaemons: vi.fn(async () => kataDaemons.rows),
}));
vi.mock("./lib/api/kata/taskClient.js", () => ({
  createKataTaskAPI: kataClients.create,
}));
vi.mock("./lib/api/kata/snapshot.js", () => ({
  searchKataTaskReferences: kataReferences.search,
}));
vi.mock("./lib/features/kata/kataAuxiliaryAuthority.svelte.js", () => ({
  createKataAuxiliaryAuthority: kataAuxiliary.create,
}));
vi.mock("./lib/api/docs/api.js", () => ({
  createDocsAPI: () => ({}),
}));
vi.mock("./lib/stores/keyboard/mode-palette-search.js", () => ({
  searchModePalette: modePalette.search,
}));
vi.mock("./lib/utils/appStartup.js", () => ({
  runAppStartup: ({
    afterBackendReady,
    onReady,
  }: {
    afterBackendReady?: (signal: AbortSignal) => void;
    onReady: () => void;
  }) => {
    const signal = new AbortController().signal;
    const markReady = () => {
      afterBackendReady?.(signal);
      onReady();
    };
    if (startup.autoReady) {
      queueMicrotask(markReady);
    } else {
      startup.readyCallbacks.push(markReady);
    }
    return vi.fn();
  },
}));

function installBrowserGlobals() {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = vi.fn();
    },
  );
}

function createAppTarget() {
  const target = document.createElement("div");
  target.id = "app";
  document.body.appendChild(target);
  return target;
}

describe("App feature routes", () => {
  beforeEach(async () => {
    vi.clearAllMocks();
    featureImports.docs = 0;
    featureImports.failDocsOnce = false;
    startup.autoReady = true;
    startup.readyCallbacks = [];
    kataDaemons.rows = [];
    kataAuxiliary.instance.issues = [];
    appSurfaceProps.palette = null;
    appSurfaceProps.docs = null;
    appSurfaceProps.provider = null;
    kataReferences.search.mockResolvedValue({
      server_instance_id: "server-a",
      daemon_id: "home",
      generation: 7,
      invalidation_epoch: 2,
      fetched_at: "2026-07-20T12:00:00Z",
      references: [],
    });
    installBrowserGlobals();
    window.history.replaceState(null, "", "/pulls");
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    const { resetKataDaemonRoster, setActiveKataDaemon } = await import("./lib/stores/active-kata-daemon.svelte.js");
    resetKataDaemonRoster();
    setActiveKataDaemon(undefined, false);
    replaceUrl("/pulls");
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("retries lazy feature imports after a chunk load failure", async () => {
    featureImports.failDocsOnce = true;
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });

    await waitFor(() => expect(featureImports.docs).toBe(1));
    expect(screen.getByText(/\[vitest\] There was an error when mocking a module/)).toBeTruthy();
    expect(featureImports.docs).toBe(1);

    await fireEvent.click(screen.getByRole("button", { name: "Retry loading Docs" }));

    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());
    expect(featureImports.docs).toBe(2);
  }, 10_000);

  it("waits for app readiness before mounting lazy feature shells", async () => {
    startup.autoReady = false;
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { fetchKataDaemons } = await import("./lib/api/kata/daemons.js");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.getByText("Loading")).toBeTruthy());
    await waitFor(() => expect(featureImports.docs).toBe(1));

    expect(screen.queryByTestId("docs-feature")).toBeNull();
    expect(fetchKataDaemons).not.toHaveBeenCalled();

    for (const onReady of startup.readyCallbacks) {
      onReady();
    }
    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());
    await waitFor(() => expect(fetchKataDaemons).toHaveBeenCalledTimes(1));
  });

  it("keeps Docs mounted while hidden", async () => {
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.queryByText("Loading")).toBeNull());

    const { navigate } = await import("./lib/stores/router.svelte.ts");

    navigate("/docs?folder=notes&doc=README.md");
    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Docs count 0" }));
    expect(document.querySelector("[data-testid='docs-feature'] button")?.textContent).toContain("Docs count 1");

    navigate("/kata");
    await waitFor(() => expect(document.querySelector(".docs-shell")?.hasAttribute("hidden")).toBe(true));
    expect(document.querySelector("[data-testid='docs-feature'] button")?.textContent).toContain("Docs count 1");

    navigate("/docs?folder=notes&doc=guide.md");
    await waitFor(() => expect(document.querySelector(".docs-shell")?.hasAttribute("hidden")).toBe(false));
    expect(document.querySelector("[data-testid='docs-feature'] button")?.textContent).toContain("Docs count 1");
  });

  it("renders flashes raised through the shared store in the app-mounted kit banner", async () => {
    const { default: App } = await import("./App.svelte");
    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.queryByText("Loading")).toBeNull());

    // Import through the same facade every caller uses. This guards the
    // single-module-instance invariant the flash unification depends on: if
    // frontend and packages/ui ever resolve different kit-ui copies, the
    // flash lands in a store the mounted banner does not read and this fails.
    const { showFlash, getFlashes, dismissFlash } = await import("@kenn-forge/ui/stores/flash");
    try {
      showFlash("first shared-store flash");
      await waitFor(() => expect(screen.getByText("first shared-store flash")).toBeTruthy());

      // Stacking (not latest-wins replacement) is the intended semantics of
      // the kit store; both flashes stay visible.
      showFlash("second shared-store flash");
      await waitFor(() => expect(screen.getByText("second shared-store flash")).toBeTruthy());
      expect(screen.getByText("first shared-store flash")).toBeTruthy();
    } finally {
      for (const flash of getFlashes()) dismissFlash(flash.id);
    }
  });

  it("routes Provider warnings through a warning-tone flash", async () => {
    const { default: App } = await import("./App.svelte");
    render(App, { target: createAppTarget() });
    await waitFor(() => expect(appSurfaceProps.provider).not.toBeNull());
    const onWarning = appSurfaceProps.provider?.onWarning as ((message: string) => void) | undefined;
    const { getFlashes, dismissFlash } = await import("@kenn-forge/ui/stores/flash");

    try {
      onWarning?.("Merged, but workspace cleanup failed");
      expect(getFlashes()).toContainEqual(
        expect.objectContaining({
          message: "Merged, but workspace cleanup failed",
          tone: "warning",
        }),
      );
    } finally {
      for (const flash of getFlashes()) dismissFlash(flash.id);
    }
  });

  it("isolates the Kata workspace client from cross-surface searches", async () => {
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/kata");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.queryByText("Loading")).toBeNull());

    expect(kataClients.create).toHaveBeenCalledTimes(2);
  });

  it("shares one auxiliary Kata authority with the palette", async () => {
    kataDaemons.rows = [
      { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
      { id: "work", url: "http://127.0.0.1:7778", default: false, auth: "none", health: "connected" },
    ];
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { default: App } = await import("./App.svelte");

    const { unmount } = render(App, { target: createAppTarget() });
    await waitFor(() => expect(appSurfaceProps.docs).not.toBeNull());
    await waitFor(() => expect(kataAuxiliary.instance.load).toHaveBeenCalledWith("home"));

    const modeSearch = appSurfaceProps.palette?.modeSearch as ((query: string) => Promise<unknown>) | undefined;
    expect(modeSearch).toBeTypeOf("function");
    await modeSearch?.("linked task");

    expect(kataAuxiliary.create).toHaveBeenCalledOnce();
    expect(modePalette.search).toHaveBeenCalledWith("linked task", {
      kata: kataAuxiliary.instance,
      docs: expect.any(Object),
    });

    const { setActiveKataDaemon } = await import("./lib/stores/active-kata-daemon.svelte.js");
    setActiveKataDaemon("work", false);
    await waitFor(() => expect(kataAuxiliary.instance.load).toHaveBeenCalledWith("work"));

    unmount();
    expect(kataAuxiliary.instance.stop).toHaveBeenCalledOnce();
  });

  it("opens a cross-surface Kata task in an authority that contains it", async () => {
    kataDaemons.rows = [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }];
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(appSurfaceProps.palette).not.toBeNull());

    const openKataIssue = appSurfaceProps.palette?.onOpenKataIssue as
      | ((target: { uid: string; status: "open" | "closed"; project_uid: string }) => void)
      | undefined;
    openKataIssue?.({
      uid: "issue-closed",
      status: "closed",
      project_uid: "project-target",
    });

    expect(window.location.pathname + window.location.search).toBe(
      "/kata?view=logbook&scope=project-target&issue=issue-closed",
    );
  });

  it("resolves cross-surface navigation authority through an isolated selection", async () => {
    kataDaemons.rows = [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }];
    // A stale shared-snapshot row must not short-circuit routing authority:
    // the task was reopened and closed again, so only the isolated selection
    // carries its current status.
    kataAuxiliary.instance.issues = [{ uid: "issue-closed", status: "open", project_uid: "project-stale" }] as never;
    kataAuxiliary.instance.selectIssue.mockResolvedValue({
      daemonID: "home",
      detail: { issue: { uid: "issue-closed", status: "closed", project_uid: "project-target" } },
    });
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(appSurfaceProps.docs).not.toBeNull());

    const openIssue = appSurfaceProps.docs?.onOpenIssue as ((uid: string) => void) | undefined;
    openIssue?.("issue-closed");

    await waitFor(() =>
      expect(window.location.pathname + window.location.search).toBe(
        "/kata?view=logbook&scope=project-target&issue=issue-closed&daemon=home",
      ),
    );
    expect(kataAuxiliary.instance.selectIssue).toHaveBeenCalledWith("issue-closed", undefined);
  });

  it("pins docs-originated task links to the folder daemon", async () => {
    kataDaemons.rows = [
      { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
      { id: "docs-daemon", url: "http://127.0.0.1:7778", default: false, auth: "none", health: "connected" },
    ];
    kataAuxiliary.instance.selectIssue.mockResolvedValue({
      daemonID: "docs-daemon",
      detail: { issue: { uid: "issue-doc", status: "open", project_uid: "project-doc" } },
    });
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(appSurfaceProps.docs).not.toBeNull());

    const openIssue = appSurfaceProps.docs?.onOpenIssue as ((uid: string, daemonId?: string) => void) | undefined;
    expect(openIssue).toBeTypeOf("function");
    openIssue?.("issue-doc", "docs-daemon");

    await waitFor(() =>
      expect(window.location.pathname + window.location.search).toBe(
        "/kata?view=all&scope=project-doc&issue=issue-doc&daemon=docs-daemon",
      ),
    );
    expect(kataAuxiliary.instance.selectIssue).toHaveBeenCalledWith("issue-doc", "docs-daemon");
  });

  it("ignores an older auxiliary selection that resolves after a newer navigation", async () => {
    kataDaemons.rows = [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }];
    let resolveOlder!: (value: unknown) => void;
    const older = new Promise((resolve) => {
      resolveOlder = resolve;
    });
    kataAuxiliary.instance.selectIssue.mockReturnValueOnce(older as never).mockResolvedValueOnce({
      daemonID: "home",
      detail: { issue: { uid: "issue-new", status: "open", project_uid: "project-new" } },
    });
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(appSurfaceProps.docs).not.toBeNull());

    const openIssue = appSurfaceProps.docs?.onOpenIssue as ((uid: string) => void) | undefined;
    openIssue?.("issue-old");
    openIssue?.("issue-new");

    await waitFor(() =>
      expect(window.location.pathname + window.location.search).toBe(
        "/kata?view=all&scope=project-new&issue=issue-new&daemon=home",
      ),
    );

    resolveOlder({
      daemonID: "home",
      detail: { issue: { uid: "issue-old", status: "closed", project_uid: "project-old" } },
    });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(window.location.pathname + window.location.search).toBe(
      "/kata?view=all&scope=project-new&issue=issue-new&daemon=home",
    );
  });

  it("handles an initial auxiliary authority load rejection at the app lifecycle boundary", async () => {
    kataDaemons.rows = [{ id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" }];
    const catchRejection = vi.fn(() => Promise.resolve(false));
    const handled = { catch: catchRejection } as unknown as Promise<boolean>;
    kataAuxiliary.instance.load.mockReturnValueOnce(handled);
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });

    await waitFor(() => expect(kataAuxiliary.instance.load).toHaveBeenCalledWith("home"));
    expect(catchRejection).toHaveBeenCalledOnce();
  });

  it("routes open textual Kata references through the generated reference search", async () => {
    kataReferences.search.mockResolvedValueOnce({
      server_instance_id: "server-a",
      daemon_id: "home",
      generation: 7,
      invalidation_epoch: 2,
      fetched_at: "2026-07-20T12:00:00Z",
      references: [
        {
          uid: "issue-solo",
          project_id: 7,
          project_uid: "project-a",
          project_name: "Project A",
          short_id: "solo",
          qualified_id: "Project A#solo",
          reference: "solo",
          title: "Open task",
          status: "open",
        },
      ],
    });
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Open Kata reference" }));

    await waitFor(() =>
      expect(window.location.pathname + window.location.search).toBe("/kata?view=all&scope=project-a&issue=issue-solo"),
    );
    expect(kataReferences.search).toHaveBeenCalledWith("solo", { status: "all" });
  });

  it("routes completed textual Kata references through all-status resolution", async () => {
    kataReferences.search.mockResolvedValueOnce({
      server_instance_id: "server-a",
      daemon_id: "home",
      generation: 7,
      invalidation_epoch: 2,
      fetched_at: "2026-07-20T12:00:00Z",
      references: [
        {
          uid: "issue-closed",
          project_id: 7,
          project_uid: "project-a",
          project_name: "Project A",
          short_id: "closed-task",
          qualified_id: "Project A#closed-task",
          reference: "closed-task",
          title: "Completed task",
          status: "closed",
        },
      ],
    });
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Open closed Kata reference" }));

    await waitFor(() =>
      expect(window.location.pathname + window.location.search).toBe(
        "/kata?view=logbook&scope=project-a&issue=issue-closed",
      ),
    );
    expect(kataReferences.search).toHaveBeenCalledWith("closed-task", { status: "all" });
  });

  it("does not route an ambiguous bare reference from a qualified server result", async () => {
    kataReferences.search.mockResolvedValueOnce({
      server_instance_id: "server-a",
      daemon_id: "home",
      generation: 7,
      invalidation_epoch: 2,
      fetched_at: "2026-07-20T12:00:00Z",
      references: [
        {
          uid: "issue-ambiguous",
          project_id: 7,
          project_uid: "project-a",
          project_name: "Project A",
          short_id: "solo",
          qualified_id: "Project A#solo",
          reference: "Project A#solo",
          title: "Ambiguous task",
          status: "open",
        },
      ],
    });
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/docs?folder=notes&doc=README.md");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Open Kata reference" }));

    await waitFor(() => expect(kataReferences.search).toHaveBeenCalledWith("solo", { status: "all" }));
    expect(window.location.pathname + window.location.search).toBe("/docs?folder=notes&doc=README.md");
  });
});
