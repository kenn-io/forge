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

async function openMenu(): Promise<void> {
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
        { label: "triage", agent: "codex", prompt: "Triage this change" },
      ],
      onQuickAction: () => {},
      onQuickActionInSession: () => {},
      currentSession: { label: "Helper" },
    },
  });
  await page.getByRole("button", { name: "Quick actions" }).click();
}

// Whether the pointer can hover belongs to the browser page, which spec files
// share. A spec that emulates touch leaves hover off for every file after it:
// turning the emulation off does not bring hover back, even in a new frame.
// So this spec emulates nothing and checks the rule for the page it was given.
const canHover = matchMedia("(hover: hover)").matches;

it.runIf(canHover)("shows a row's send button only while that row is hovered or keyboard-focused", async () => {
  await openMenu();
  // Opening focuses the first row. Whether a browser counts that as keyboard
  // focus depends on the window being active, so the rows below it carry the
  // resting state.
  expect([opacity("Send review to Helper"), opacity("Send triage to Helper")]).toEqual(["0", "0"]);

  await userEvent.hover(page.getByRole("button", { name: "review", exact: true }));
  await expect.poll(() => [opacity("Send review to Helper"), opacity("Send triage to Helper")]).toEqual(["1", "0"]);

  await userEvent.hover(page.getByRole("button", { name: "Quick actions" }));
  await expect.poll(() => opacity("Send review to Helper")).toBe("0");

  page.getByRole("button", { name: "review", exact: true }).element().focus();
  await userEvent.tab();
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Send review to Helper");
  await expect.poll(() => [opacity("Send review to Helper"), opacity("Send triage to Helper")]).toEqual(["1", "0"]);
});

it.runIf(!canHover)("keeps every send button visible when the pointer cannot hover", async () => {
  await openMenu();
  expect([opacity("Send review to Helper"), opacity("Send triage to Helper")]).toEqual(["1", "1"]);
});
