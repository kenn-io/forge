// The send button is hidden until its row is pointed at or reached by keyboard.
// That is paint, which jsdom cannot see: its click reaches an invisible button.
import { mount, unmount } from "svelte";
import { afterEach, expect, it } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import "@kenn-io/kit-ui/theme.css";

import LaunchMenu from "./LaunchMenu.svelte";

let app: Record<string, unknown> | undefined;
let host: HTMLElement | undefined;

afterEach(async () => {
  if (app) await unmount(app);
  host?.remove();
  app = undefined;
  host = undefined;
});

function opacity(name: string): string {
  return getComputedStyle(page.getByRole("button", { name }).element()).opacity;
}

it("shows a row's send button only while that row is hovered or keyboard-focused", async () => {
  host = document.createElement("div");
  host.style.cssText = "position: fixed; left: 300px; top: 20px;";
  document.body.append(host);
  app = mount(LaunchMenu, {
    target: host,
    props: {
      showLaunch: false,
      launchTargets: [{ key: "codex", label: "Codex", kind: "agent", source: "builtin", available: true }],
      quickActions: [
        { label: "audit", agent: "codex", prompt: "Audit this change" },
        { label: "review", agent: "codex", prompt: "Review this change" },
      ],
      onQuickAction: () => {},
      onQuickActionInSession: () => {},
      currentSession: { label: "Helper" },
    },
  });
  await page.getByRole("button", { name: "Quick actions" }).click();

  // Opening focuses the first row, which must not reveal its button.
  expect(opacity("Send audit to Helper")).toBe("0");
  expect(opacity("Send review to Helper")).toBe("0");

  await userEvent.hover(page.getByRole("button", { name: "review", exact: true }));
  await expect.poll(() => opacity("Send review to Helper")).toBe("1");
  expect(opacity("Send audit to Helper")).toBe("0");

  await userEvent.unhover(page.getByRole("button", { name: "review", exact: true }));
  await expect.poll(() => opacity("Send review to Helper")).toBe("0");
  await userEvent.keyboard("{Tab}");
  await expect.poll(() => opacity("Send audit to Helper")).toBe("1");
});
