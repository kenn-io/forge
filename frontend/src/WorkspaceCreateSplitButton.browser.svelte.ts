import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { cleanup, render } from "vitest-browser-svelte";
import "./app.css";

import { Button, setThemeMode } from "@kenn-io/kit-ui";
import type { LaunchTarget } from "./lib/api/types.js";
import { STORES_KEY } from "./lib/context.js";
import WorkspaceCreateSplitButtonRuntimeHarness from "./lib/components/workspace/WorkspaceCreateSplitButtonRuntimeHarness.svelte";
import { createMockApiFetch, jsonResponse } from "./test/mockApiFetch.js";
import NewWorkspaceDialogRuntimeHarness from "./test/NewWorkspaceDialogRuntimeHarness.svelte";

const launchTargets: LaunchTarget[] = [
  {
    key: "codex",
    label: "Codex",
    kind: "agent",
    source: "builtin",
    command: ["codex"],
    available: true,
    disabled_reason: "",
  },
];

function resolvedColor(value: string): string {
  const probe = document.createElement("span");
  probe.style.color = value;
  document.body.append(probe);
  const color = getComputedStyle(probe).color;
  probe.remove();
  return color;
}

describe("workspace create split button control height", () => {
  afterEach(cleanup);

  // The detail action rows put this split button beside plain "sm" kit buttons
  // (Close issue, Reopen issue, Open Workspace). It used to pin its own 30px
  // min-height and ignore the requested size, so it stood taller than every
  // neighbour. Both segments must now measure exactly like a sibling button of
  // the same size, which only real CSS can tell us.
  for (const size of ["sm", "md"] as const) {
    it(`matches a sibling ${size} kit button on both segments`, async () => {
      await render(WorkspaceCreateSplitButtonRuntimeHarness, {
        props: { label: "Create Workspace", size, launchTargets, onCreate: () => {} },
      });
      await render(Button, { props: { size, label: "Close issue" } });

      const sibling = page.getByRole("button", { name: "Close issue" });
      await expect.element(sibling).toBeVisible();

      const expected = sibling.element().getBoundingClientRect().height;
      const primary = page.getByRole("button", { name: "Create Workspace", exact: true }).element();
      const options = page.getByRole("button", { name: "Create Workspace options" }).element();

      expect(primary.getBoundingClientRect().height).toBeCloseTo(expected, 1);
      expect(options.getBoundingClientRect().height).toBeCloseTo(expected, 1);
    });
  }
});

describe("workspace create split button in the New workspace dialog", () => {
  const originalFetch = globalThis.fetch;

  afterEach(async () => {
    cleanup();
    globalThis.fetch = originalFetch;
    setThemeMode("light");
  });

  it("renders the launch menu outside the real modal body and keeps its items clickable", async () => {
    const api = createMockApiFetch([
      ({ method, url }) =>
        method === "POST" && url.pathname.endsWith("/workspaces") ? jsonResponse({ id: "ws-new" }, 202) : undefined,
    ]);
    globalThis.fetch = api.fetch;
    const onCreated = vi.fn();

    await render(NewWorkspaceDialogRuntimeHarness, {
      props: { open: true, onClose: vi.fn(), onCreated },
      context: new Map([
        [
          STORES_KEY,
          {
            settings: {
              getLaunchTargets: () => launchTargets,
              getWorkspaceSettings: () => ({ default_execution_target: "" }),
            },
          },
        ],
      ]),
    });

    const dialog = page.getByRole("dialog", { name: "New workspace" });
    await expect.element(dialog).toBeVisible();
    await vi.waitFor(() =>
      expect(dialog.getByRole("button", { name: "Create workspace", exact: true }).element()).not.toBeDisabled(),
    );

    const options = dialog.getByRole("button", { name: "Create workspace options" });
    await options.click();
    const item = page.getByRole("menuitem", { name: "Codex" });
    await expect.element(item).toBeVisible();

    const menu = item.element().closest<HTMLElement>("[role='menu']");
    const panel = document.querySelector<HTMLElement>(".kit-modal-panel");
    const body = document.querySelector<HTMLElement>(".kit-modal-body");
    expect(menu?.parentElement).toBe(panel);
    expect(menu?.parentElement).not.toBe(body);

    const itemRect = item.element().getBoundingClientRect();
    const hit = document.elementFromPoint(itemRect.left + itemRect.width / 2, itemRect.top + itemRect.height / 2);
    expect(item.element() === hit || item.element().contains(hit)).toBe(true);

    await item.click();
    await vi.waitFor(() => expect(onCreated).toHaveBeenCalledWith("ws-new", undefined));
  });

  it.each([
    ["github.com", false, "Devbox is in maintenance"],
    ["github.example.com", true, "Devboxes currently support only github.com"],
  ])(
    "explains an unavailable devbox and allows choosing this machine (%s)",
    async (platformHost, available, reason) => {
      const api = createMockApiFetch([
        ({ method, url }) => {
          if (method === "POST" && url.pathname.endsWith("/workspaces")) return jsonResponse({ id: "ws-local" }, 202);
          if (method !== "GET") return undefined;
          if (url.pathname === "/api/v1/repos")
            return jsonResponse([
              {
                ID: 1,
                Owner: "acme",
                Name: "widget",
                Platform: "github",
                PlatformHost: platformHost,
                Host: platformHost,
                DefaultBranch: "main",
                IsArchived: false,
              },
            ]);
          if (url.pathname === "/api/v1/snapshot")
            return jsonResponse({
              hosts: [
                {
                  configKey: "local",
                  kind: "self",
                  name: "Laptop",
                  federationRole: "hub",
                  operationAvailability: { workspaceWrite: { available: true } },
                },
                {
                  configKey: "devbox:compute-a",
                  kind: "devbox",
                  name: "Compute A",
                  federationRole: "devbox",
                  operationAvailability: { workspaceWrite: { available, unavailableReason: reason } },
                },
              ],
            });
          return undefined;
        },
      ]);
      globalThis.fetch = api.fetch;
      const onCreated = vi.fn();
      await render(NewWorkspaceDialogRuntimeHarness, {
        props: { open: true, onClose: vi.fn(), onCreated },
        context: new Map([
          [
            STORES_KEY,
            {
              settings: {
                getLaunchTargets: () => launchTargets,
                getWorkspaceSettings: () => ({ default_execution_target: "devbox:compute-a" }),
              },
            },
          ],
        ]),
      });
      const dialog = page.getByRole("dialog", { name: "New workspace" });
      await expect.element(dialog.getByText(reason, { exact: false })).toBeVisible();
      await expect.element(dialog.getByRole("button", { name: "Create workspace", exact: true })).toBeDisabled();
      await dialog.getByRole("combobox", { name: "Workspace machine: Compute A (devbox)" }).click();
      await expect.element(page.getByRole("option", { name: "Compute A (devbox)", exact: false })).toBeDisabled();
      await page.getByRole("option", { name: "Laptop (this machine)" }).click();
      await dialog.getByRole("button", { name: "Create workspace", exact: true }).click();
      await vi.waitFor(() => expect(onCreated).toHaveBeenCalledWith("ws-local", undefined));
    },
  );

  it("uses kit-ui primary colors for both solid segments", async () => {
    globalThis.fetch = createMockApiFetch().fetch;
    setThemeMode("dark");

    await render(NewWorkspaceDialogRuntimeHarness, {
      props: { open: true, onClose: vi.fn(), onCreated: vi.fn() },
      context: new Map([
        [
          STORES_KEY,
          {
            settings: {
              getLaunchTargets: () => launchTargets,
              getWorkspaceSettings: () => ({ default_execution_target: "" }),
            },
          },
        ],
      ]),
    });

    const dialog = page.getByRole("dialog", { name: "New workspace" });
    await expect.element(dialog).toBeVisible();
    await vi.waitFor(() =>
      expect(dialog.getByRole("button", { name: "Create workspace", exact: true }).element()).not.toBeDisabled(),
    );

    const primary = dialog.getByRole("button", { name: "Create workspace", exact: true }).element();
    const options = dialog.getByRole("button", { name: "Create workspace options" }).element();
    expect(primary.classList.contains("kit-button")).toBe(true);
    expect(options.classList.contains("kit-button")).toBe(true);

    const primaryStyle = getComputedStyle(primary);
    const optionsStyle = getComputedStyle(options);
    const rootStyle = getComputedStyle(document.documentElement);
    const accent = resolvedColor(rootStyle.getPropertyValue("--accent-blue").trim());
    const foreground = resolvedColor(rootStyle.getPropertyValue("--bg-surface").trim());

    expect(primaryStyle.backgroundColor).toBe(accent);
    expect(optionsStyle.backgroundColor).toBe(accent);
    expect(primaryStyle.color).toBe(foreground);
    expect(optionsStyle.color).toBe(foreground);
  });

  it("keeps the launch split button usable for the Kata issue source", async () => {
    const api = createMockApiFetch([
      ({ method, url }) =>
        method === "GET" && url.pathname.endsWith("/kata/daemons")
          ? jsonResponse({
              daemons: [
                {
                  id: "healthy",
                  url: "http://kata.test",
                  health: "connected",
                  auth: "none",
                  default: true,
                  api_schema_version: "0.10.0",
                },
              ],
            })
          : undefined,
      ({ method, url }) =>
        method === "GET" && url.pathname.endsWith("/kata/daemons/healthy/references")
          ? jsonResponse({
              issues: [
                {
                  uid: "issue-1",
                  project_uid: "project-1",
                  project_name: "Kata",
                  qualified_id: "Kata#KT-1",
                  short_id: "KT-1",
                  status: "open",
                  title: "Keep one UI",
                },
              ],
            })
          : undefined,
      ({ method, url }) =>
        method === "POST" && url.pathname.endsWith("/kata/workspaces")
          ? jsonResponse({ id: "ws-kata", created: true }, 202)
          : undefined,
    ]);
    globalThis.fetch = api.fetch;
    const onCreated = vi.fn();

    await render(NewWorkspaceDialogRuntimeHarness, {
      props: { open: true, initialSource: "kata_issue", onClose: vi.fn(), onCreated },
      context: new Map([
        [
          STORES_KEY,
          {
            settings: {
              getLaunchTargets: () => launchTargets,
              getWorkspaceSettings: () => ({ default_execution_target: "" }),
            },
          },
        ],
      ]),
    });

    const dialog = page.getByRole("dialog", { name: "New workspace" });
    await expect.element(dialog.getByRole("combobox", { name: /Kata daemon: healthy/ })).toBeVisible();
    await dialog.getByRole("searchbox", { name: "Search Kata issues" }).fill("keep");
    await dialog.getByRole("button", { name: /Kata#KT-1 Keep one UI/ }).click();

    const primary = dialog.getByRole("button", { name: "Create or open workspace", exact: true });
    await expect.element(primary).not.toBeDisabled();
    await dialog.getByRole("button", { name: "Create or open workspace options" }).click();
    await page.getByRole("menuitem", { name: "Codex" }).click();

    await vi.waitFor(() => expect(onCreated).toHaveBeenCalledWith("ws-kata", undefined));
  });
});
