import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Prevent RepoTypeahead from making real API calls in the test environment.
vi.mock("../../api/runtime.js", () => ({
  client: {
    GET: () => Promise.resolve({ data: [], error: undefined }),
  },
  apiErrorMessage: () => "",
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
    }),
  };
});

import AppHeader from "./AppHeader.svelte";
import { initTheme, cleanupTheme } from "../../stores/theme.svelte.js";
import { setSidebarCollapsed } from "../../stores/sidebar.svelte.ts";
import { navigate } from "../../stores/router.svelte.ts";

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

describe("AppHeader", () => {
  beforeEach(() => {
    document.documentElement.classList.remove("dark");
    localStorage.clear();
    mockMatchMedia(false);
    setSidebarCollapsed(false);
  });

  afterEach(() => {
    cleanupTheme();
    cleanup();
    navigate("/");
    document.documentElement.classList.remove("dark");
    localStorage.clear();
    setSidebarCollapsed(false);
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
    expect(localStorage.getItem("middleman-theme")).toBeNull();
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

    expect(
      container.querySelector("button[title='Toggle theme'] svg"),
    ).toBeTruthy();
    expect(
      container.querySelector("button[title='Settings'] svg"),
    ).toBeTruthy();
    expect(
      container.querySelector("button[title='Select repository'] svg"),
    ).toBeTruthy();
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

    const moon = screen.getByTitle("Toggle theme").querySelector(
      "[data-filled-icon='moon'] svg",
    );

    expect(moon).toBeTruthy();
  });

  it("returns to the previous page when the settings button is clicked again", async () => {
    initTheme();
    navigate("/pulls/github/acme/widgets/1/files");
    render(AppHeader);

    await fireEvent.click(screen.getByTitle("Settings"));
    expect(window.location.pathname + window.location.search).toBe("/settings");

    await fireEvent.click(screen.getByTitle("Settings"));
    expect(window.location.pathname + window.location.search).toBe(
      "/pulls/github/acme/widgets/1/files",
    );
  });

  it("renders the collapsed sidebar toggle as a header icon button", () => {
    initTheme();
    setSidebarCollapsed(true);
    const { container } = render(AppHeader);

    expect(
      container.querySelector("button[title='Expand sidebar'] svg"),
    ).toBeTruthy();
  });

  it("opens selected Activity PR in PRs tab with files tab preserved", async () => {
    initTheme();
    navigate("/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&selected_tab=files");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "PRs" }));

    expect(window.location.pathname + window.location.search).toBe(
      "/pulls/github/acme/widgets/1/files",
    );
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

  it("opens Issues list when Activity selection is a PR", async () => {
    initTheme();
    navigate("/?selected=pr:1&provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&selected_tab=files");
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Issues" }));

    expect(window.location.pathname + window.location.search).toBe("/issues");
  });
});
