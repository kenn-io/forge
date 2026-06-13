// Vitest conversion of frontend/tests/e2e-full/navigation.spec.ts (app-shell
// cases: header tabs, Kata shortcuts, direct mode-shell loads, /mail legacy
// route, settings toggle). The PR/issue row -> detail-pane cases live in
// packages/ui/src/views/{PRListView,IssueListView}.row-select.test.ts.
//
// Unlike App.test.ts this suite keeps the REAL AppHeader (and real router and
// keyboard dispatch) so .view-tab clicks and the Settings button drive actual
// navigation; only the data-heavy views/features are stubbed. Shell-invariant
// module mocks come from the appShellMocks preset.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import "./lib/testing/appShellMocks.js";
import "./lib/testing/appShellInstantStartupMock.js";
import { createAppTarget, installBrowserGlobals } from "./lib/testing/appShellMocks.js";

vi.mock("@middleman/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@middleman/ui")>();
  const Provider = (await import("./lib/testing/AppProviderMock.svelte")).default;
  const Stub = (await import("./lib/testing/AppViewStub.svelte")).default;
  const KindStub = (await import("./lib/testing/AppViewKindStub.svelte")).default;
  return {
    ...actual,
    Provider,
    PRListView: KindStub,
    IssueListView: KindStub,
    ActivityFeedView: KindStub,
    FocusListView: KindStub,
    MobileActivityView: Stub,
    KanbanBoardView: Stub,
    ReviewsView: Stub,
    // The real getStores reads Svelte context set by the real Provider;
    // AppProviderMock does not set it, so AppHeader gets these instead.
    getStores: () => ({
      sync: {
        getSyncState: () => null,
        triggerSync: () => Promise.resolve(),
      },
      settings: {
        isModeVisible: () => true,
      },
    }),
  };
});

function currentPath(): string {
  return window.location.pathname + window.location.search;
}

async function renderAppAt(path: string) {
  const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
  replaceUrl(path);
  const { default: App } = await import("./App.svelte");
  render(App, { target: createAppTarget() });
}

function pressKey(key: string): void {
  window.dispatchEvent(new KeyboardEvent("keydown", { key }));
}

describe("App view navigation", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.classList.remove("dark");
    installBrowserGlobals();
    window.history.replaceState(null, "", "/");
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    localStorage.clear();
    document.documentElement.classList.remove("dark");
  });

  it("header tabs switch between views", async () => {
    await renderAppAt("/");
    await waitFor(() => expect(screen.getByTestId("view-stub-activity")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Kata" }));
    expect(currentPath()).toBe("/kata");
    await waitFor(() => expect(screen.getByTestId("kata-workspace-stub")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Docs" }));
    expect(currentPath()).toBe("/docs");
    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Messages" }));
    expect(currentPath()).toBe("/messages");
    await waitFor(() => expect(screen.getByTestId("messages-feature")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "PRs" }));
    expect(currentPath()).toBe("/pulls");
    await waitFor(() => expect(screen.getByTestId("view-stub-pulls")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Issues" }));
    expect(currentPath()).toBe("/issues");
    await waitFor(() => expect(screen.getByTestId("view-stub-issues")).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Activity" }));
    expect(window.location.pathname).toBe("/");
    await waitFor(() => expect(screen.getByTestId("view-stub-activity")).toBeTruthy());
  });

  it("Kata shell does not expose repo selector or respond to PR number shortcuts", async () => {
    await renderAppAt("/kata");
    await waitFor(() => expect(screen.getByTestId("kata-workspace-stub")).toBeTruthy());

    expect(currentPath()).toBe("/kata");
    expect(screen.queryByTitle("Select repository")).toBeNull();

    pressKey("1");
    expect(currentPath()).toBe("/kata");

    pressKey("2");
    expect(currentPath()).toBe("/kata");

    // Control: the same shortcuts are live on the issues page, proving the
    // /kata assertions above exercise a real (not unwired) dispatch path.
    const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
    replaceUrl("/issues");
    pressKey("1");
    expect(currentPath()).toBe("/pulls");
    pressKey("2");
    expect(currentPath()).toBe("/pulls/board");

    // And the repo selector exists on provider pages, so its absence on
    // /kata is meaningful.
    replaceUrl("/pulls");
    await waitFor(() => expect(screen.getByTitle("Select repository")).toBeTruthy());
  });

  it("Docs and Messages routes load their mode shells directly", async () => {
    await renderAppAt("/docs");
    expect(currentPath()).toBe("/docs");
    await waitFor(() => expect(screen.getByTestId("docs-feature")).toBeTruthy());

    cleanup();

    await renderAppAt("/messages");
    expect(currentPath()).toBe("/messages");
    await waitFor(() => expect(screen.getByTestId("messages-feature")).toBeTruthy());
  });

  it("old mail route does not open Messages", async () => {
    await renderAppAt("/mail?q=label%3AInbox");

    // The unknown /mail path falls through to the activity view.
    await waitFor(() => expect(screen.getByTestId("view-stub-activity")).toBeTruthy());
    expect(screen.queryByTestId("messages-feature")).toBeNull();
  });

  it("settings button toggles back to the previous route", async () => {
    await renderAppAt("/pulls/github/acme/widgets/1/files");
    await waitFor(() => expect(screen.getByTestId("view-stub-pulls")).toBeTruthy());
    expect(currentPath()).toBe("/pulls/github/acme/widgets/1/files");

    await fireEvent.click(screen.getByTitle("Settings"));
    expect(currentPath()).toBe("/settings");
    await waitFor(() => expect(screen.getByTestId("settings-page-stub")).toBeTruthy());

    await fireEvent.click(screen.getByTitle("Settings"));
    expect(currentPath()).toBe("/pulls/github/acme/widgets/1/files");
    await waitFor(() => expect(screen.getByTestId("view-stub-pulls")).toBeTruthy());
    // Dropped vs the Playwright spec: the trailing ".diff-file visible"
    // check; diff rendering stays covered by the diff-view Playwright spec.
  });
});
