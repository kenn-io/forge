import { showWorkspaceLaunchError } from "../../app/workspace-telemetry.js";
import { dismissFlash } from "../../stores/flash.svelte.js";
import { cleanup, render, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { tick } from "svelte";
import { expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../../app/runtime.js";
import { startTestTelemetry } from "../../testing/telemetry.js";
import Harness from "./ErrorShownTestHarness.svelte";

it.each([2, 3])("reports a visible error once across %s presenters, then counts retries", async (copies) => {
  const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  const runtime = makeAppRuntime();
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  const view = render(Harness, { runtime, shown: false, copies });
  try {
    await tick();
    await view.rerender({ runtime, shown: true, copies });
    expect(sent).not.toHaveBeenCalled();
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    await waitFor(() =>
      expect(sent).toHaveBeenCalledExactlyOnceWith(
        { event: "error_shown", properties: { surface: "web", kind: "pr_viewed" } },
        {},
      ),
    );
    await view.rerender({ runtime, copies: 1 });
    await view.rerender({ runtime, copies });
    expect(sent).toHaveBeenCalledTimes(1);
    await view.rerender({ runtime, copies, retrying: true });
    expect(sent).toHaveBeenCalledTimes(1);
    await view.rerender({ runtime, copies, retrying: false });
    await waitFor(() => expect(sent).toHaveBeenCalledTimes(2));
    await view.rerender({ runtime, mounted: false });
    await view.rerender({ runtime, mounted: true });
    await waitFor(() => expect(sent).toHaveBeenCalledTimes(3));
  } finally {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
    vi.restoreAllMocks();
  }
});

it("counts launch flashes only while presented and drops flashes that expire while hidden", async () => {
  const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  const runtime = makeAppRuntime();
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  render(Harness, { runtime, flashesOnly: true });
  try {
    showWorkspaceLaunchError("Launch denied");
    await tick();
    expect(sent).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(4000);
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    await tick();
    expect(sent).not.toHaveBeenCalled();
    visibility.mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    showWorkspaceLaunchError("Retry denied");
    await tick();
    expect(sent).not.toHaveBeenCalled();
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    await tick();
    expect(sent).toHaveBeenCalledExactlyOnceWith(
      { event: "error_shown", properties: { surface: "web", kind: "workspace_opened" } },
      {},
    );
  } finally {
    cleanup();
    dismissFlash();
    vi.useRealTimers();
    vi.restoreAllMocks();
    await Effect.runPromise(runtime.disposeEffect);
  }
});
