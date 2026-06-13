// Vitest conversion of frontend/tests/e2e-full/force-mobile-routes.spec.ts:
// with __MIDDLEMAN_FORCE_MOBILE_ROUTES__ set on a desktop-sized viewport,
// the canonical /issues route must keep its URL but switch to the FOCUS
// presentation (FocusListView inside App's .focus-layout main) instead of
// the desktop AppHeader + IssueListView shell or the .mobile-shell.
//
// View components are mocked with AppViewKindStub, which derives a
// distinguishable testid from the props App passes to each view, so the
// assertions distinguish WHICH presentation App picked. The Playwright
// spec's ".focus-layout .focus-list" check maps to the focus stub being
// inside main.focus-layout (FocusListView's internal .focus-list markup is
// covered by FocusListView's own tests).
//
// Shell-invariant module mocks come from the appShellMocks preset; only the
// suite-specific surfaces (@middleman/ui view mapping, AppHeader stub) are
// mocked here.
import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import "./lib/testing/appShellMocks.js";
import "./lib/testing/appShellInstantStartupMock.js";
import { createAppTarget, installBrowserGlobals } from "./lib/testing/appShellMocks.js";

vi.mock("@middleman/ui", async () => {
  const Provider = (await import("./lib/testing/AppProviderMock.svelte")).default;
  const Stub = (await import("./lib/testing/AppViewStub.svelte")).default;
  const KindStub = (await import("./lib/testing/AppViewKindStub.svelte")).default;
  return {
    Provider,
    PRListView: KindStub,
    IssueListView: KindStub,
    ActivityFeedView: KindStub,
    FocusListView: KindStub,
    MobileActivityView: Stub,
    KanbanBoardView: Stub,
    ReviewsView: Stub,
  };
});

// AppHeader gets no props from App, so the kind stub renders
// data-testid="view-stub-unknown" for it: a unique marker in this suite
// (every other KindStub use receives view props and maps to a named kind).
// The desktop control test below proves the marker shows up when the
// header IS rendered, so asserting its absence under the flag is meaningful.
vi.mock("./lib/components/layout/AppHeader.svelte", async () => ({
  default: (await import("./lib/testing/AppViewKindStub.svelte")).default,
}));

async function renderAppAtIssues() {
  const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
  replaceUrl("/issues");
  const { default: App } = await import("./App.svelte");
  render(App, { target: createAppTarget() });
}

type ForceMobileWindow = Window & { __MIDDLEMAN_FORCE_MOBILE_ROUTES__?: boolean };

describe("force-mobile routes flag", () => {
  beforeEach(() => {
    localStorage.clear();
    // Desktop environment: every media query (including "(pointer: coarse)")
    // reports false, matching the Playwright spec's 1280x800 viewport.
    installBrowserGlobals();
    window.history.replaceState(null, "", "/");
  });

  afterEach(() => {
    cleanup();
    delete (window as ForceMobileWindow).__MIDDLEMAN_FORCE_MOBILE_ROUTES__;
    vi.unstubAllGlobals();
    localStorage.clear();
  });

  it("control: without the flag, /issues renders the desktop shell", async () => {
    await renderAppAtIssues();

    await waitFor(() => expect(screen.getByTestId("view-stub-issues")).toBeTruthy());
    expect(window.location.pathname).toBe("/issues");
    // AppHeader (kind stub without props) is part of the desktop shell.
    expect(screen.getByTestId("view-stub-unknown")).toBeTruthy();
    expect(document.querySelector(".focus-layout")).toBeNull();
    expect(document.querySelector(".mobile-shell")).toBeNull();
    expect(screen.queryByTestId("view-stub-focus-issues")).toBeNull();
  });

  it("renders the canonical issue route with the focus presentation", async () => {
    (window as ForceMobileWindow).__MIDDLEMAN_FORCE_MOBILE_ROUTES__ = true;

    await renderAppAtIssues();

    // URL stays canonical /issues (no /m redirect).
    const focusStub = await waitFor(() => screen.getByTestId("view-stub-focus-issues"));
    expect(window.location.pathname).toBe("/issues");

    // FocusListView mounts inside App's .focus-layout main, with the phone
    // layout class the flag forces.
    const layout = focusStub.closest("main.focus-layout");
    expect(layout).not.toBeNull();
    expect(layout?.classList.contains("focus-layout--phone")).toBe(true);

    // Not the mobile shell, not the desktop shell.
    expect(document.querySelector(".mobile-shell")).toBeNull();
    expect(screen.queryByTestId("view-stub-unknown")).toBeNull();
    expect(screen.queryByTestId("view-stub-issues")).toBeNull();
  });
});
