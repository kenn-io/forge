import { cleanup, fireEvent, render, screen, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataDaemonInfo } from "../../api/kata/daemons.js";
import KataDaemonSwitcher from "./KataDaemonSwitcher.svelte";

const daemons: KataDaemonInfo[] = [
  {
    id: "home",
    url: "http://127.0.0.1:7777",
    default: true,
    auth: "none",
    health: "connected",
  },
  {
    id: "work",
    url: "https://work.example",
    default: false,
    auth: "oidc",
    health: "auth_required",
  },
];

describe("KataDaemonSwitcher", () => {
  afterEach(() => {
    cleanup();
  });

  it("shows the active daemon id on the chip", () => {
    render(KataDaemonSwitcher, { props: { daemons, activeId: "home", onSelect: () => {} } });

    const chip = screen.getByRole("button", { name: "Switch Kata daemon: home" });
    expect(chip.textContent).toContain("home");
    expect(chip.querySelector(".chip-icon")).toBeTruthy();
    expect(screen.queryByText("Daemon")).toBeNull();
  });

  it("keeps daemon health indicators inside the menu", async () => {
    const { container } = render(KataDaemonSwitcher, {
      props: { daemons, activeId: "home", onSelect: () => {} },
    });

    expect(screen.getByTestId("daemon-chip").querySelector(".dot")).toBeNull();

    await fireEvent.click(screen.getByTestId("daemon-chip"));

    expect(screen.queryByText("Switch daemon")).toBeNull();
    expect(screen.queryByText("Configured Kata daemons")).toBeNull();
    expect(within(screen.getByTestId("daemon-row-home")).getByText("connected")).toBeTruthy();
    expect(within(screen.getByTestId("daemon-row-work")).getByText("needs auth")).toBeTruthy();
    expect(container.querySelector(".daemon-menu .dot")).toBeTruthy();
  });

  it("clicking a daemon row calls onSelect with its id", async () => {
    const onSelect = vi.fn();
    render(KataDaemonSwitcher, { props: { daemons, activeId: "home", onSelect } });

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    await fireEvent.click(screen.getByTestId("daemon-row-work"));

    expect(onSelect).toHaveBeenCalledWith("work");
  });

  it("renders a daemon's operator hint when present", async () => {
    const withHint: KataDaemonInfo[] = [
      {
        id: "local",
        url: "",
        default: true,
        auth: "none",
        health: "down",
        hint: "local daemon not running; run `kata daemon start`",
      },
    ];
    render(KataDaemonSwitcher, { props: { daemons: withHint, activeId: undefined, onSelect: () => {} } });

    await fireEvent.click(screen.getByTestId("daemon-chip"));

    expect(screen.getByText(/kata daemon start/).textContent).toContain("kata daemon start");
  });

  it("opens the menu from the chip's left edge so it stays usable near the viewport edge", async () => {
    const { container } = render(KataDaemonSwitcher, { props: { daemons, activeId: "home", onSelect: () => {} } });

    await fireEvent.click(screen.getByTestId("daemon-chip"));

    const menu = container.querySelector(".daemon-menu");
    expect(menu).toBeTruthy();
    expect(menu!.getAttribute("data-align")).toBe("start");
  });
});
