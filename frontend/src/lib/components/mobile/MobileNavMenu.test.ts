import { fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { Page } from "../../stores/router.svelte.js";
import MobileNavMenuTestHarness from "./MobileNavMenuTestHarness.svelte";

function nav(page: Page, overrides: { hidden?: string[] } = {}) {
  return {
    page: () => page,
    isModeVisible: (mode: string) => !(overrides.hidden ?? []).includes(mode),
    onNavigate: vi.fn(),
    onDesktopView: vi.fn(),
  };
}

async function openMenu(): Promise<void> {
  await fireEvent.click(screen.getByRole("button", { name: "Menu" }));
}

afterEach(() => vi.unstubAllGlobals());

describe("MobileNavMenu", () => {
  it("returns to native connection settings through the menu", async () => {
    const postMessage = vi.fn();
    vi.stubGlobal("ReactNativeWebView", { postMessage });
    render(MobileNavMenuTestHarness, { props: { nav: nav("mobile-workspaces") } });
    await openMenu();
    await fireEvent.click(screen.getByRole("button", { name: "Connection settings" }));
    expect(postMessage).toHaveBeenCalledWith(JSON.stringify({ type: "connection-settings" }));
    expect(screen.queryByRole("dialog", { name: "Menu" })).toBeNull();
  });

  it("renders nothing outside the phone shell", () => {
    render(MobileNavMenuTestHarness, { props: {} });

    expect(screen.queryByRole("button", { name: "Menu" })).toBeNull();
  });

  it("marks the workspace family current for workspace terminal routes", async () => {
    render(MobileNavMenuTestHarness, { props: { nav: nav("mobile-workspace-terminal") } });

    await openMenu();

    const current = screen.getByRole("navigation", { name: "Phone mode" }).querySelector("[aria-current='page']");
    expect(current?.textContent?.trim()).toBe("Workspaces");
  });

  it("navigates to a mode, excludes hidden modes, and closes the sheet", async () => {
    const props = nav("mobile-workspaces", { hidden: ["issues"] });
    render(MobileNavMenuTestHarness, { props: { nav: props } });

    await openMenu();
    expect(screen.queryByRole("button", { name: "Issues" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "PRs" }));

    expect(props.onNavigate).toHaveBeenCalledWith("/m/pulls");
    expect(screen.queryByRole("navigation", { name: "Phone mode" })).toBeNull();
  });

  it("opens the desktop view from the sheet", async () => {
    const props = nav("mobile-activity");
    render(MobileNavMenuTestHarness, { props: { nav: props } });

    await openMenu();
    await fireEvent.click(screen.getByRole("button", { name: "Open desktop view" }));

    expect(props.onDesktopView).toHaveBeenCalledOnce();
    await openMenu();
    expect(screen.queryByRole("button", { name: "Connection settings" })).toBeNull();
  });
});
