import { cleanup, render, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { tick } from "svelte";
import { expect, it, vi } from "vite-plus/test";
import type { PullDetail } from "../../api/types.js";
import { makeAppRuntime } from "../../app/runtime.js";
import { makeGeneratedClient } from "../../testing/generated-client.js";
import { createMockApiFetch } from "../../../test/mockApiFetch.js";
import Harness from "./PullDetailPaneTelemetryHarness.svelte";

vi.mock("../diff/DiffFilesLayout.svelte", async () => ({
  default: (await import("../diff/DiffFilesLayoutTestView.svelte")).default,
}));

it.each(["detail", "files"])("counts PR %s when the pane and document are visible", async (content) => {
  const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  const capture = vi.fn(async () => ({ status: "queued" }));
  const runtime = makeAppRuntime(makeGeneratedClient({ SystemService: { captureTelemetryEvent: capture } }));
  const api = createMockApiFetch();
  const detail = (await (await api.fetch("/api/v1/pulls/github/acme/widgets/42")).json()) as PullDetail;
  const paneProps = {
    // An inactive tab body keeps this test focused on the pane's reporting effect.
    tabKey: "inactive",
    keyboardActive: false,
    visible: false,
    pr: { provider: "github", owner: "acme", name: "widgets", repoPath: "acme/widgets", number: 42 },
    detail: null as PullDetail | null,
  };
  const view = render(Harness, { props: { runtime, paneProps } });
  const showTab = async () => {
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    await tick();
  };
  try {
    await view.rerender({ runtime, paneProps: { ...paneProps, detail } });
    await tick();
    expect(capture).not.toHaveBeenCalled();
    await view.rerender({ runtime, paneProps: { ...paneProps, visible: true } });
    await showTab();
    expect(capture).not.toHaveBeenCalled();
    visibility.mockReturnValue("hidden");
    await view.rerender({
      runtime,
      paneProps: { ...paneProps, visible: true, tabKey: content === "files" ? "files" : "inactive", detail: content === "files" ? null : detail },
    });
    await tick();
    expect(capture).not.toHaveBeenCalled();
    await showTab();
    await waitFor(() => expect(capture).toHaveBeenCalledTimes(1));
    expect(capture.mock.calls[0]?.[0]).toMatchObject({
      event: "pr_viewed",
      properties: { provider: "github", surface: "web" },
    });
  } finally {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
    vi.restoreAllMocks();
  }
});
