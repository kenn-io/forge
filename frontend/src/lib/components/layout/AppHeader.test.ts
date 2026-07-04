import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { compile } from "svelte/compiler";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import headerIconButtonSource from "./HeaderIconButton.svelte?raw";

const mockedContainerSize = vi.hoisted(() => ({
  value: "wide" as "narrow" | "medium" | "wide",
}));

type ModeKey =
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

const mockedModeVisibility = vi.hoisted(() => ({
  value: {
    activity: true,
    repos: true,
    kata: false,
    docs: false,
    messages: false,
    pulls: true,
    issues: true,
    board: true,
    reviews: true,
    workspaces: true,
  } as Record<ModeKey, boolean>,
}));

// Prevent RepoTypeahead from making real API calls in the test environment.
vi.mock("../../api/runtime.js", () => ({
  client: {
    GET: () => Promise.resolve({ data: [], error: undefined }),
  },
  apiErrorMessage: () => "",
}));

vi.mock("../../stores/container.svelte.js", () => ({
  getContainerSize: () => mockedContainerSize.value,
  isNarrow: () => mockedContainerSize.value === "narrow",
}));

// AppHeader reads sync state from the @middleman/ui context.
vi.mock("@middleman/ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@middleman/ui")>();
  return {
    ...actual,
    getStores: () => ({
      sync: {
        getSyncState: () => null,
        triggerSync: () => Promise.resolve(),
      },
      settings: {
        isModeVisible: (mode: ModeKey) => mockedModeVisibility.value[mode],
      },
    }),
  };
});

import AppHeader from "./AppHeader.svelte";
import { initTheme, cleanupTheme } from "../../stores/theme.svelte.js";
import { setSidebarCollapsed } from "../../stores/sidebar.svelte.ts";
import { navigate } from "../../stores/router.svelte.ts";
import { isPaletteOpen, resetPaletteState } from "../../stores/keyboard/palette-state.svelte.js";

function compiledStyle(source: string, selector: string): CSSStyleDeclaration {
  const css = compile(source, { filename: "component.svelte" }).css?.code ?? "";
  const style = document.createElement("style");
  style.textContent = css;
  document.head.appendChild(style);

  for (const rule of Array.from(style.sheet?.cssRules ?? [])) {
    if (!("selectorText" in rule) || !("style" in rule)) continue;
    if (String(rule.selectorText).includes(selector)) {
      return rule.style as CSSStyleDeclaration;
    }
  }
  throw new Error(`Could not find compiled style rule for ${selector}`);
}

type MediaChangeCallback = (event: MediaQueryListEvent) => void;

function mockMatchMedia(matches: boolean, listeners?: MediaChangeCallback[]): void {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn().mockImplementation((_event: string, cb: MediaChangeCallback) => {
        listeners?.push(cb);
      }),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

function showImportedModes(): void {
  mockedModeVisibility.value = {
    ...mockedModeVisibility.value,
    kata: true,
    docs: true,
    messages: true,
  };
}

function expectReservedRepoSelectorSlot(container: HTMLElement): void {
  const slot = container.querySelector(".repo-selector-placeholder");

  expect(screen.queryByTitle("Select repository")).toBeNull();
  expect(slot?.getAttribute("aria-hidden")).toBe("true");
}

describe("AppHeader", () => {
  beforeEach(() => {
    document.documentElement.classList.remove("dark");
    localStorage.clear();
    mockMatchMedia(false);
    setSidebarCollapsed(false);
    mockedContainerSize.value = "wide";
    delete window.__middleman_config;
    window.__middleman_notify_config_changed?.();
    mockedModeVisibility.value = {
      activity: true,
      repos: true,
      kata: false,
      docs: false,
      messages: false,
      pulls: true,
      issues: true,
      board: true,
      reviews: true,
      workspaces: true,
    };
    resetPaletteState();
  });

  afterEach(() => {
    cleanupTheme();
    cleanup();
    navigate("/");
    document.documentElement.classList.remove("dark");
    localStorage.clear();
    setSidebarCollapsed(false);
    mockedContainerSize.value = "wide";
    delete window.__middleman_config;
    window.__middleman_notify_config_changed?.();
    mockedModeVisibility.value = {
      activity: true,
      repos: true,
      kata: false,
      docs: false,
      messages: false,
      pulls: true,
      issues: true,
      board: true,
      reviews: true,
      workspaces: true,
    };
    resetPaletteState();
  });

  it("toggles the root dark class when the theme button is clicked", async () => {
    initTheme();
    render(AppHeader);

    const button = screen.getByTitle("Toggle theme");

    expect(document.documentElement.classList.contains("dark")).toBe(false);

    await fireEvent.click(button);
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    await fireEvent.click(button);
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("applies the system dark preference on mount", () => {
    cleanup();
    document.documentElement.classList.remove("dark");
    mockMatchMedia(true);

    initTheme();
    render(AppHeader);

    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("persists theme choice to localStorage on toggle", async () => {
    initTheme();
    render(AppHeader);

    const button = screen.getByTitle("Toggle theme");

    await fireEvent.click(button);
    expect(localStorage.getItem("middleman-theme")).toBe("dark");

    await fireEvent.click(button);
    expect(localStorage.getItem("middleman-theme")).toBe("light");
  });

  it("restores theme from localStorage over system preference", () => {
    localStorage.setItem("middleman-theme", "dark");
    mockMatchMedia(false);

    initTheme();
    render(AppHeader);

    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("falls back to system preference when no stored theme", () => {
    cleanup();
    document.documentElement.classList.remove("dark");
    mockMatchMedia(true);

    initTheme();
    render(AppHeader);

    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("ignores invalid localStorage value and falls back to system preference", () => {
    cleanup();
    document.documentElement.classList.remove("dark");
    localStorage.setItem("middleman-theme", "garbage");
    mockMatchMedia(true);

    initTheme();
    render(AppHeader);

    expect(document.documentElement.classList.contains("dark")).toBe(true);
    // kit-ui's store leaves the invalid value in place (it resolves to
    // "system" on every read); the old store deleted it. Either way the next
    // explicit toggle overwrites it, so only the fallback is contractual.
    localStorage.removeItem("middleman-theme");
  });

  it("falls back to system preference when localStorage throws", () => {
    cleanup();
    document.documentElement.classList.remove("dark");
    mockMatchMedia(true);

    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("blocked");
    });

    initTheme();
    render(AppHeader);

    expect(document.documentElement.classList.contains("dark")).toBe(true);

    vi.restoreAllMocks();
  });

  it("toggle still works when localStorage.setItem throws", async () => {
    initTheme();

    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("blocked");
    });

    render(AppHeader);

    const button = screen.getByTitle("Toggle theme");

    await fireEvent.click(button);
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    vi.restoreAllMocks();
  });

  it("renders SVG icons for the header controls", () => {
    initTheme();
    const { container } = render(AppHeader);

    expect(container.querySelector("button[title='Open command palette'] svg")).toBeTruthy();
    expect(container.querySelector("button[title='Toggle theme'] svg")).toBeTruthy();
    expect(container.querySelector("button[title='Settings'] svg")).toBeTruthy();
    expect(container.querySelector("button[title='Select repository'] svg")).toBeTruthy();
  });

  it("spaces the command palette icon and shortcut hint", () => {
    const buttonStyle = compiledStyle(headerIconButtonSource, "button");

    expect(buttonStyle.getPropertyValue("gap")).toBe("var(--space-3)");
  });

  it("opens the command palette from the header trigger", async () => {
    initTheme();
    render(AppHeader);

    expect(isPaletteOpen()).toBe(false);

    await fireEvent.click(screen.getByRole("button", { name: "Open command palette" }));

    expect(isPaletteOpen()).toBe(true);
  });

  it("changes the theme toggle SVG when toggled", async () => {
    initTheme();
    render(AppHeader);

    const button = screen.getByTitle("Toggle theme");
    const before = button.querySelector("svg")?.innerHTML ?? null;

    expect(before).toBeTruthy();

    await fireEvent.click(button);

    const after = button.querySelector("svg")?.innerHTML ?? null;

    expect(after).toBeTruthy();
    expect(after).not.toBe(before);
  });

  it("renders a filled moon icon in light mode", () => {
    initTheme();
    render(AppHeader);

    const moon = screen.getByTitle("Toggle theme").querySelector("[data-filled-icon='moon'] svg");

    expect(moon).toBeTruthy();
  });

  it("returns to the previous page when the settings button is clicked again", async () => {
    initTheme();
    navigate("/pulls/github/acme/widgets/1/files");
    render(AppHeader);

    await fireEvent.click(screen.getByTitle("Settings"));
    expect(window.location.pathname + window.location.search).toBe("/settings");

    await fireEvent.click(screen.getByTitle("Settings"));
    expect(window.location.pathname + window.location.search).toBe("/pulls/github/acme/widgets/1/files");
  });

  it("renders the collapsed sidebar toggle as a header icon button", () => {
    initTheme();
    setSidebarCollapsed(true);
    const { container } = render(AppHeader);

    expect(container.querySelector("button[title='Expand sidebar'] svg")).toBeTruthy();
  });

  it("marks the Workspaces tab current on terminal routes", () => {
    // One tabs list drives both the expanded tab row and kit's collapsed
    // dropdown, so the terminal → workspaces active mapping only needs
    // asserting once. jsdom cannot trigger kit TopBar's measurement
    // collapse (zero-width layout), so the expanded row is the testable
    // presentation here; the collapsed dropdown is covered by the
    // container-layout e2e spec.
    initTheme();
    navigate("/terminal/ws-123");
    render(AppHeader);

    const workspacesTab = screen.getByRole("button", { name: "Workspaces" });
    expect(workspacesTab.getAttribute("aria-current")).toBe("page");
  });

  it("does not show the collapsed sidebar shortcut hint on Activity", () => {
    initTheme();
    navigate("/");
    setSidebarCollapsed(true);
    const { container } = render(AppHeader);

    const expandButton = container.querySelector("button[title='Expand sidebar']");
    expect(expandButton).toBeTruthy();
    expect(expandButton!.querySelector("kbd[aria-label]")).toBeNull();
  });

  it("hides imported modes from the top nav by default", () => {
    initTheme();
    render(AppHeader);

    expect(screen.queryByRole("button", { name: "Kata" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Docs" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Messages" })).toBeNull();
  });

  it("navigates to Kata from the desktop nav", async () => {
    initTheme();
    showImportedModes();
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Kata" }));

    expect(window.location.pathname + window.location.search).toBe("/kata");
  });

  it("resets an active sticky mode tab to its default route", async () => {
    initTheme();
    showImportedModes();
    navigate("/kata?issue=issue-q3");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Kata" }));

    expect(window.location.pathname + window.location.search).toBe("/kata");
  });

  it("navigates to Docs from the desktop nav", async () => {
    initTheme();
    showImportedModes();
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Docs" }));

    expect(window.location.pathname + window.location.search).toBe("/docs");
  });

  it("navigates to Messages from the desktop nav", async () => {
    initTheme();
    showImportedModes();
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Messages" }));

    expect(window.location.pathname + window.location.search).toBe("/messages");
  });

  it("reserves the provider repo selector slot on Kata without exposing the selector", () => {
    initTheme();
    navigate("/kata");
    const { container } = render(AppHeader);

    expectReservedRepoSelectorSlot(container);
  });

  it("reserves the provider repo selector slot on Docs without exposing the selector", () => {
    initTheme();
    navigate("/docs");
    const { container } = render(AppHeader);

    expectReservedRepoSelectorSlot(container);
  });

  it("reserves the provider repo selector slot on Messages without exposing the selector", () => {
    initTheme();
    navigate("/messages");
    const { container } = render(AppHeader);

    expectReservedRepoSelectorSlot(container);
  });

  it("does not reserve the repo selector slot when embed config hides it", () => {
    initTheme();
    window.__middleman_config = { ui: { hideRepoSelector: true } };
    window.__middleman_notify_config_changed?.();
    navigate("/kata");
    const { container } = render(AppHeader);

    expect(screen.queryByTitle("Select repository")).toBeNull();
    expect(container.querySelector(".repo-selector-placeholder")).toBeNull();
  });

  it("remembers sticky mode routes when the nav switches to Activity", async () => {
    initTheme();
    showImportedModes();
    navigate("/kata?issue=issue-q3");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Activity" }));

    expect(window.location.pathname + window.location.search).toBe("/");

    await fireEvent.click(screen.getByRole("button", { name: "Kata" }));

    expect(window.location.pathname + window.location.search).toBe("/kata?issue=issue-q3");
  });

  it("opens selected Activity PR in PRs tab with files tab preserved", async () => {
    initTheme();
    navigate("/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&selected_tab=files");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "PRs" }));

    expect(window.location.pathname + window.location.search).toBe("/pulls/github/acme/widgets/1/files");
  });

  it("opens selected Activity issue in Issues tab with platform host preserved", async () => {
    initTheme();
    navigate("/?selected=issue:10&provider=github&platform_host=ghe.example.com&repo_path=acme%2Fwidgets");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Issues" }));

    expect(window.location.pathname + window.location.search).toBe(
      "/host/ghe.example.com/issues/github/acme/widgets/10",
    );
  });

  it("restores the previous Activity view when returning from PRs", async () => {
    initTheme();
    navigate("/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&range=30d");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "PRs" }));
    expect(window.location.pathname).toBe("/pulls/github/acme/widgets/1");

    await fireEvent.click(screen.getByRole("button", { name: "Activity" }));

    expect(window.location.pathname + window.location.search).toBe(
      "/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&range=30d",
    );
  });

  it("restores the previous Activity view when returning from the settings gear", async () => {
    initTheme();
    navigate("/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets");
    render(AppHeader);

    // The settings gear leaves Activity without going through navigateTab.
    await fireEvent.click(screen.getByTitle("Settings"));
    expect(window.location.pathname).toBe("/settings");

    await fireEvent.click(screen.getByRole("button", { name: "Activity" }));

    expect(window.location.pathname + window.location.search).toBe(
      "/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets",
    );
  });

  it("restores the previous Activity view when returning from Repos", async () => {
    initTheme();
    navigate("/?range=90d&view=threaded");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Repos" }));
    expect(window.location.pathname).toBe("/repos");

    await fireEvent.click(screen.getByRole("button", { name: "Activity" }));

    expect(window.location.pathname + window.location.search).toBe("/?range=90d&view=threaded");
  });

  it("opens Issues list when Activity selection is a PR", async () => {
    initTheme();
    navigate("/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&selected_tab=files");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Issues" }));

    expect(window.location.pathname + window.location.search).toBe("/issues");
  });
});
