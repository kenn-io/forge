import { afterEach, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import { cleanup, render } from "vitest-browser-svelte";
import "./app.css";

import { STORES_KEY } from "./lib/context.js";
import { createMockApiFetch, jsonResponse } from "./test/mockApiFetch.js";
import NewWorkspaceDialogRuntimeHarness from "./test/NewWorkspaceDialogRuntimeHarness.svelte";

const originalFetch = globalThis.fetch;

afterEach(() => {
  cleanup();
  globalThis.fetch = originalFetch;
  localStorage.removeItem("kenn-forge:workspace:new_repo");
});

it("selects a repository by typing after tabbing into the closed picker", async () => {
  const api = createMockApiFetch([
    ({ method, url }) =>
      method === "GET" && url.pathname === "/api/v1/repos"
        ? jsonResponse([
            {
              ID: 1,
              Owner: "acme",
              Name: "widget",
              Platform: "github",
              PlatformHost: "github.com",
              PlatformRepoID: 1001,
            },
            {
              ID: 2,
              Owner: "acme",
              Name: "gadget",
              Platform: "github",
              PlatformHost: "github.com",
              PlatformRepoID: 1002,
            },
          ])
        : undefined,
  ]);
  globalThis.fetch = api.fetch;
  localStorage.removeItem("kenn-forge:workspace:new_repo");
  const onClose = vi.fn();

  await render(NewWorkspaceDialogRuntimeHarness, {
    props: { open: true, onClose, onCreated: vi.fn() },
    context: new Map([
      [
        STORES_KEY,
        { settings: { getLaunchTargets: () => [], getWorkspaceSettings: () => ({ default_execution_target: "" }) } },
      ],
    ]),
  });

  const picker = page.getByRole("button", { name: "Filter repositories: acme/widget" });
  await expect.element(picker).toBeVisible();
  page.getByRole("button", { name: "Kata issue", exact: true }).element().focus();
  await userEvent.keyboard("{Tab}");
  await expect.element(picker).toHaveFocus();
  await userEvent.keyboard("gadget");

  const search = page.getByRole("combobox", { name: "Filter repositories" });
  await expect.element(search).toHaveFocus();
  await expect.element(search).toHaveValue("gadget");
  await expect.element(page.getByRole("option", { name: /^acme\/gadget\b/ })).toBeVisible();
  await expect.element(page.getByRole("option", { name: /^acme\/widget\b/ })).not.toBeInTheDocument();
  await userEvent.keyboard("{Escape}");
  await expect.element(picker).toHaveFocus();
  expect(onClose).not.toHaveBeenCalled();

  await userEvent.keyboard("gadget");
  await expect.element(search).toHaveValue("gadget");
  await userEvent.keyboard("{Enter}");
  await expect.element(page.getByRole("button", { name: "Filter repositories: acme/gadget" })).toHaveFocus();
  expect(onClose).not.toHaveBeenCalled();
  await userEvent.keyboard("{Tab}");
  await expect.element(page.getByRole("textbox", { name: "Branch name" })).toHaveFocus();
});
