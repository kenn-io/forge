import { afterEach, expect, it, vi } from "vite-plus/test";
import { page, userEvent } from "vite-plus/test/browser";
import { cleanup, render } from "vitest-browser-svelte";
import { Effect } from "effect";
import "../../../app.css";
import { makeTestAppRuntime } from "../../testing/effect-layers.js";
import { makeGeneratedClient } from "../../testing/generated-client.js";
import DocsWorkspace from "./DocsWorkspaceTestHarness.svelte";
import { createMockDocsBackend } from "./docsTestBackend.js";

vi.mock("../../api/kata/integration.js", () => ({
  fetchKataDaemons: async () => [],
  searchKataReferences: async () => [],
}));

let runtime: ReturnType<typeof makeTestAppRuntime>;
afterEach(async () => {
  cleanup();
  if (runtime) await Effect.runPromise(runtime.disposeEffect);
});

it("reports an empty Docs filename query when the field is committed", async () => {
  const sent = vi.fn(async () => ({ status: "queued" as const }));
  runtime = makeTestAppRuntime(makeGeneratedClient({ SystemService: { captureTelemetryEvent: sent } }));
  render(DocsWorkspace, {
    props: {
      route: { mode: "docs", folder: "notes", doc: "README.md" },
      onRouteChange: vi.fn(),
      api: createMockDocsBackend(),
      runtime,
    },
  });
  await page.getByRole("treeitem", { name: "README.md" }).click();
  await userEvent.keyboard("z");
  const input = page.getByPlaceholder("Search…");
  await input.fill("zxq-no-file");
  await new Promise((resolve) => setTimeout(resolve, 500));
  expect(sent).not.toHaveBeenCalled();
  await userEvent.keyboard("{Tab}");
  await expect
    .poll(() => sent.mock.calls)
    .toEqual([[{ event: "search_empty", properties: { surface: "web" } }, expect.anything()]]);
});

it("cancels a filename search when its folder tree is replaced", async () => {
  const sent = vi.fn(async () => ({ status: "queued" as const }));
  runtime = makeTestAppRuntime(makeGeneratedClient({ SystemService: { captureTelemetryEvent: sent } }));
  const props = {
    route: { mode: "docs" as const, folder: "notes", doc: "README.md" },
    onRouteChange: vi.fn(),
    api: createMockDocsBackend(),
    runtime,
  };
  const view = await render(DocsWorkspace, { props });
  await page.getByRole("treeitem", { name: "README.md" }).click();
  await userEvent.keyboard("z");
  await page.getByPlaceholder("Search…").fill("zxq-no-file");
  await view.rerender({ route: { mode: "docs", folder: "engineering", doc: null } });
  await expect.element(page.getByRole("treeitem", { name: "index.md" })).toBeVisible();
  await new Promise((resolve) => setTimeout(resolve, 450));
  expect(sent).not.toHaveBeenCalled();
});
