// Converted from frontend/tests/e2e-full/link-navigation-repo-sync.spec.ts.
//
// Regression coverage: deep-linking to a PR or issue used to leave the
// repo dropdown pinned to whichever repo was previously picked, even
// though the detail pane jumped to the new repo. App.svelte's
// syncGlobalRepoWithRoute effect now follows the route's selected item.
//
// The list views are stubbed, but the route -> global repo wiring under
// test (the $effect calling syncGlobalRepoWithRoute) is real App code,
// and the AppHeader (with the real RepoTypeahead dropdown) renders
// unmocked so the .typeahead-value assertions match the e2e spec.
// Shell-invariant module mocks come from the appShellMocks preset.
//
// Dropped from the spec: the sidebar `.repo-header__name` count/text
// sub-assertion. The sidebar list is a stubbed PRListView here and its
// repo filtering happens server-side; the repo filter value those lists
// receive is exactly getGlobalRepo(), which is asserted directly.
import { cleanup, render, waitFor } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import "./lib/testing/appShellMocks.js";
import "./lib/testing/appShellInstantStartupMock.js";
import { createAppTarget, installBrowserGlobals } from "./lib/testing/appShellMocks.js";

vi.mock("@middleman/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@middleman/ui")>();
  const Provider = (await import("./lib/testing/AppProviderConfiguredMock.svelte")).default;
  const { configuredMockStores } = await import("./lib/testing/appProviderConfiguredStores.js");
  const Stub = (await import("./lib/testing/AppViewStub.svelte")).default;
  return {
    ...actual,
    Provider,
    // The mocked Provider never seeds the real context, so components
    // that read stores via getStores (AppHeader, RepoTypeahead) get the
    // same configured mock stores App binds.
    getStores: () => configuredMockStores,
    PRListView: Stub,
    IssueListView: Stub,
    ActivityFeedView: Stub,
    MobileActivityView: Stub,
    KanbanBoardView: Stub,
    ReviewsView: Stub,
    FocusListView: Stub,
  };
});

import { getGlobalRepo, setGlobalRepo } from "./lib/stores/filter.svelte.ts";

const STORAGE_KEY = "middleman-filter-repo";

function typeaheadValue(): string | undefined {
  const values = document.querySelectorAll(".typeahead-value");
  expect(values.length).toBe(1);
  return values[0]?.textContent?.trim();
}

async function renderApp(path: string) {
  const { replaceUrl } = await import("./lib/stores/router.svelte.ts");
  replaceUrl(path);
  const { default: App } = await import("./App.svelte");
  render(App, { target: createAppTarget() });
}

describe("deep-link repo dropdown sync", () => {
  beforeEach(() => {
    installBrowserGlobals();
    localStorage.clear();
    setGlobalRepo(undefined);
    window.history.replaceState(null, "", "/pulls");
  });

  afterEach(() => {
    cleanup();
    for (const el of Array.from(document.querySelectorAll("#app"))) {
      el.remove();
    }
    vi.unstubAllGlobals();
  });

  it("navigating to a PR in a different repo updates the global repo and dropdown", async () => {
    setGlobalRepo("github.com/acme/widgets");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("github.com/acme/widgets");

    await renderApp("/pulls/github/acme/tools/1");

    await waitFor(() => expect(getGlobalRepo()).toBe("github.com/acme/tools"));
    expect(localStorage.getItem(STORAGE_KEY)).toBe("github.com/acme/tools");
    await waitFor(() => expect(typeaheadValue()).toBe("github.com/acme/tools"));
  });

  it("navigating to an issue in a different repo updates the global repo and dropdown", async () => {
    setGlobalRepo("github.com/acme/tools");

    await renderApp("/issues/github/acme/widgets/10");

    await waitFor(() => expect(getGlobalRepo()).toBe("github.com/acme/widgets"));
    expect(localStorage.getItem(STORAGE_KEY)).toBe("github.com/acme/widgets");
    await waitFor(() => expect(typeaheadValue()).toBe("github.com/acme/widgets"));
  });

  it("navigating between PRs in different repos updates the repo each time", async () => {
    setGlobalRepo("github.com/acme/widgets");

    await renderApp("/pulls/github/acme/widgets/1");

    await waitFor(() => expect(typeaheadValue()).toBe("github.com/acme/widgets"));
    expect(getGlobalRepo()).toBe("github.com/acme/widgets");

    const { navigate } = await import("./lib/stores/router.svelte.ts");
    navigate("/pulls/github/acme/tools/1");

    await waitFor(() => expect(getGlobalRepo()).toBe("github.com/acme/tools"));
    expect(localStorage.getItem(STORAGE_KEY)).toBe("github.com/acme/tools");
    await waitFor(() => expect(typeaheadValue()).toBe("github.com/acme/tools"));
  });

  it("selecting an item from All repos keeps the all-repo filter", async () => {
    // No persisted repo: the filter starts as "All repos".
    await renderApp("/pulls");

    await waitFor(() => expect(typeaheadValue()).toBe("All repos"));

    // Click-equivalent of selecting a PR from the all-repos list.
    const { navigate } = await import("./lib/stores/router.svelte.ts");
    navigate("/pulls/github/acme/widgets/1");
    await waitFor(() => expect(window.location.pathname).toBe("/pulls/github/acme/widgets/1"));
    await tick();
    await tick();

    expect(getGlobalRepo()).toBeUndefined();
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
    expect(typeaheadValue()).toBe("All repos");
  });

  it("opening /pulls without a selection preserves the user's chosen repo", async () => {
    setGlobalRepo("github.com/acme/widgets");

    await renderApp("/pulls");

    await waitFor(() => expect(typeaheadValue()).toBe("github.com/acme/widgets"));
    await tick();
    await tick();

    expect(getGlobalRepo()).toBe("github.com/acme/widgets");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("github.com/acme/widgets");
  });
});
