// Guards the Targets remove-button reveal: hidden until its row is hovered or
// keyboard-focused on hover-capable pointers, always visible without hover.
import { Effect } from "effect";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import { cdp, page, userEvent } from "vite-plus/test/browser";
import { cleanup, render } from "vitest-browser-svelte";

import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import WorkspaceTargets from "./WorkspaceTargets.svelte";

let runtime: OwnedAppRuntime;
vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtime }));

const repo = {
  provider: "github",
  platform_host: "github.com",
  platform_repo_id: 101,
  owner: "acme",
  name: "widgets",
  repo_path: "acme/widgets",
};
const targets = [7, 8].map((number) => ({
  id: number,
  type: "issue",
  number,
  state: "open",
  title: `Issue ${number}`,
  source: "tracked",
  unavailable: false,
  url: "",
  repo,
}));

beforeEach(() => {
  runtime = makeAppRuntime();
  vi.spyOn(globalThis, "fetch").mockResolvedValue(Response.json({ targets, kata_available: false }));
});

afterEach(async () => {
  cleanup();
  await cdp().send("Emulation.setTouchEmulationEnabled", { enabled: false });
  await Effect.runPromise(runtime.disposeEffect);
  vi.restoreAllMocks();
});

async function renderTargets() {
  render(WorkspaceTargets, { workspaceID: "ws-1", onselect: vi.fn() });
  await page.getByRole("button", { name: /^Targets/ }).click();
  await expect.element(page.getByText("Issue 8")).toBeVisible();
}

function removeOpacity(number: number): string {
  const button = document.querySelector(`[aria-label="Remove Issue #${number} from targets"]`);
  if (!button) throw new Error(`missing remove button for Issue #${number}`);
  return getComputedStyle(button).opacity;
}

it("reveals a remove button only on its hovered or keyboard-focused row", async () => {
  await renderTargets();
  await page.getByRole("button", { name: /^Targets/ }).hover();
  expect([removeOpacity(7), removeOpacity(8)]).toEqual(["0", "0"]);

  await page.getByText("Issue 8").hover();
  await expect.poll(() => [removeOpacity(7), removeOpacity(8)]).toEqual(["0", "1"]);

  await page.getByRole("button", { name: /^Targets/ }).hover();
  await userEvent.tab();
  await userEvent.tab();
  expect(document.activeElement?.textContent).toContain("Issue 7");
  await expect.poll(() => [removeOpacity(7), removeOpacity(8)]).toEqual(["1", "0"]);
});

it("keeps every remove button visible when the pointer cannot hover", async () => {
  await cdp().send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 1 });
  expect(matchMedia("(hover: hover)").matches).toBe(false);
  await renderTargets();
  expect([removeOpacity(7), removeOpacity(8)]).toEqual(["1", "1"]);
});
