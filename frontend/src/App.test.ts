import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

const featureImports = vi.hoisted(() => ({
  docs: 0,
  messages: 0,
  failDocsOnce: false,
  failMessagesOnce: false,
}));

const startup = vi.hoisted(() => ({
  autoReady: true,
  readyCallbacks: [] as Array<() => void>,
}));

const messagesHealth = vi.hoisted(() => ({
  pendingCapabilities: false,
}));

vi.mock("@middleman/ui", async () => {
  const Provider = (await import("./lib/testing/AppProviderMock.svelte")).default;
  const Stub = (await import("./lib/testing/AppViewStub.svelte")).default;
  return {
    Provider,
    PRListView: Stub,
    IssueListView: Stub,
    ActivityFeedView: Stub,
    MobileActivityView: Stub,
    KanbanBoardView: Stub,
    ReviewsView: Stub,
    FocusListView: Stub,
  };
});

vi.mock("./lib/components/layout/AppHeader.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/layout/StatusBar.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
vi.mock("./lib/components/keyboard/Palette.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));
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
  return {
    default: (await import("./lib/testing/AppDocsFeatureMock.svelte")).default,
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
vi.mock("./lib/features/messages/MessagesFeature.svelte", async () => {
  featureImports.messages += 1;
  if (featureImports.failMessagesOnce) {
    featureImports.failMessagesOnce = false;
    throw new Error("messages chunk unavailable");
  }
  return {
    default: (await import("./lib/testing/AppMessagesFeatureMock.svelte")).default,
  };
});
vi.mock("./lib/features/messages/MessagesFeature.svelte?retry", async () => {
  featureImports.messages += 1;
  return {
    default: (await import("./lib/testing/AppMessagesFeatureMock.svelte")).default,
  };
});
vi.mock("./lib/features/messages/MessagesFeature.svelte?retry2", async () => {
  featureImports.messages += 1;
  return {
    default: (await import("./lib/testing/AppMessagesFeatureMock.svelte")).default,
  };
});
vi.mock("./lib/components/FlashBanner.svelte", async () => ({
  default: (await import("./lib/testing/AppViewStub.svelte")).default,
}));

vi.mock("./lib/api/kata/daemons.js", () => ({
  fetchKataDaemons: vi.fn(async () => []),
}));
vi.mock("./lib/api/kata/taskClient.js", () => ({
  createKataTaskAPI: () => ({}),
}));
vi.mock("./lib/api/docs/api.js", () => ({
  createDocsAPI: () => ({}),
}));
vi.mock("./lib/api/messages/api.js", () => ({
  createMessagesAPI: () => ({
    capabilities: vi.fn(() => {
      if (messagesHealth.pendingCapabilities) {
        return new Promise(() => {});
      }
      return Promise.resolve({
        configured: true,
        ok: true,
        features: {},
      });
    }),
  }),
}));
vi.mock("./lib/api/messages/visibility.js", () => ({
  shouldShowMessagesMode: () => true,
}));
vi.mock("./lib/messages/kataMessageLinker.js", () => ({
  createMessageIssueLinker: () => ({
    linkMessage: vi.fn(),
  }),
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
    featureImports.messages = 0;
    featureImports.failDocsOnce = false;
    featureImports.failMessagesOnce = false;
    startup.autoReady = true;
    startup.readyCallbacks = [];
    messagesHealth.pendingCapabilities = false;
    installBrowserGlobals();
    window.history.replaceState(null, "", "/pulls");
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/pulls");
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("retries lazy feature imports after a chunk load failure", async () => {
    featureImports.failMessagesOnce = true;
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/messages?q=project");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });

    await waitFor(() => expect(featureImports.messages).toBe(1));
    expect(screen.getByText(/\[vitest\] There was an error when mocking a module/)).toBeTruthy();
    expect(featureImports.messages).toBe(1);

    await fireEvent.click(screen.getByRole("button", { name: "Retry loading Messages" }));

    await waitFor(() => expect(screen.getByTestId("messages-feature")).toBeTruthy());
    expect(featureImports.messages).toBe(2);
  });

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

  it("keeps Docs and Messages mounted while hidden", async () => {
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.queryByText("Loading")).toBeNull());

    const { navigate } = await import("./lib/stores/router.svelte.ts");

    navigate("/docs?folder=notes&doc=README.md");
    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Docs count 0" }));
    expect(document.querySelector("[data-testid='docs-feature'] button")?.textContent).toContain("Docs count 1");

    navigate("/messages?q=project");
    await waitFor(() => expect(screen.getByTestId("messages-feature")).toBeTruthy());
    expect(document.querySelector(".docs-shell")?.hasAttribute("hidden")).toBe(true);
    expect(document.querySelector("[data-testid='docs-feature'] button")?.textContent).toContain("Docs count 1");

    navigate("/docs?folder=notes&doc=guide.md");
    await waitFor(() => expect(document.querySelector(".docs-shell")?.hasAttribute("hidden")).toBe(false));
    expect(document.querySelector("[data-testid='docs-feature'] button")?.textContent).toContain("Docs count 1");
  });

  it("opens Kata linked messages before Messages capabilities resolve", async () => {
    messagesHealth.pendingCapabilities = true;
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/kata");
    const { default: App } = await import("./App.svelte");

    render(App, { target: createAppTarget() });
    await waitFor(() => expect(screen.queryByText("Loading")).toBeNull());

    await fireEvent.click(screen.getByRole("button", { name: "message" }));

    expect(window.location.pathname + window.location.search).toBe("/messages?message=42");
  });
});
