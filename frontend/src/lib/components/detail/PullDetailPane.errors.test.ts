import { cleanup, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../../app/runtime.js";
import { createAppStores } from "../../app-stores.svelte.js";
import { STORES_KEY, NAVIGATE_KEY } from "../../context.js";
import { startTestTelemetry } from "../../testing/telemetry.js";
import { createMockApiFetch } from "../../../test/mockApiFetch.js";
import Harness from "./PullDetailPaneTelemetryHarness.svelte";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it.each(["initial", "refresh", "mismatch"])("counts rendered PR failures for %s reads", async (mode) => {
  let denied = mode === "initial";
  const api = createMockApiFetch([
    ({ url }) =>
      denied && /\/pulls\/github\/acme\/widgets\/\d+(?:\/diff)?$/.test(url.pathname)
        ? Response.json({ code: "forbidden", title: "Read denied", status: 403 }, { status: 403 })
        : undefined,
  ]);
  vi.stubGlobal("fetch", api.fetch);
  const runtime = makeAppRuntime();
  const { stores } = createAppStores({ runtime });
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  let paneProps = {
    tabKey: "conversation",
    visible: false,
    keyboardActive: false,
    detail: null,
    pr: { provider: "github", repoPath: "acme/widgets", owner: "acme", name: "widgets", number: 42 },
    autoSync: false as const,
  };
  const view = render(Harness, {
    props: { runtime, paneProps },
    context: new Map<symbol, unknown>([
      [STORES_KEY, stores],
      [NAVIGATE_KEY, vi.fn()],
    ]),
  });
  const errors = () => sent.mock.calls.filter(([event]) => event.event === "error_shown");
  try {
    await waitFor(() => expect(stores.detail.isDetailLoading()).toBe(false));
    if (mode !== "initial") {
      await waitFor(() => expect(stores.detail.getDetail()).not.toBeNull());
      denied = true;
      if (mode === "mismatch") {
        paneProps = { ...paneProps, pr: { ...paneProps.pr, number: 43 } };
        await view.rerender({ runtime, paneProps });
      } else {
        stores.detail.loadDetail("acme", "widgets", 42, { provider: "github", repoPath: "acme/widgets", sync: false });
      }
    }
    await screen.findByText(/Read denied/);
    expect(errors()).toHaveLength(0);
    await view.rerender({ runtime, paneProps: { ...paneProps, visible: true } });
    if (mode === "refresh") {
      expect(screen.getByTestId("detail-load-error").textContent).toContain("Showing previously loaded content");
      expect(errors()).toHaveLength(0);
    } else {
      await waitFor(() => expect(errors()).toHaveLength(1));
      stores.detail.loadDetail("acme", "widgets", paneProps.pr.number, {
        provider: "github",
        repoPath: "acme/widgets",
        sync: false,
      });
      await screen.findByText(/Read denied/);
      await waitFor(() => expect(errors()).toHaveLength(2));
      if (mode === "initial") {
        await view.rerender({ runtime, paneProps: { ...paneProps, visible: true, tabKey: "files" } });
        await waitFor(() => expect(stores.diff.getDiffError()).toContain("Read denied"));
        await screen.findByText(/Read denied/);
        await waitFor(() => expect(errors()).toHaveLength(3));
      }
    }
  } finally {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
  }
});
