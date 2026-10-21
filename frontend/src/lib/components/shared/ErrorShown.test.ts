import { cleanup, render, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { tick } from "svelte";
import { expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../../app/runtime.js";
import { startTestTelemetry } from "../../testing/telemetry.js";
import Harness from "./ErrorShownTestHarness.svelte";

it("reports a visible error once across presenters, then counts a remounted attempt", async () => {
  const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  const runtime = makeAppRuntime();
  const sent = vi.fn();
  await startTestTelemetry(runtime, sent).ready;
  const view = render(Harness, { runtime, shown: false, copies: 2 });
  try {
    await tick();
    await view.rerender({ runtime, shown: true, copies: 2 });
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
    await view.rerender({ runtime, copies: 2 });
    expect(sent).toHaveBeenCalledTimes(1);
    await view.rerender({ runtime, mounted: false });
    await view.rerender({ runtime, mounted: true });
    await waitFor(() => expect(sent).toHaveBeenCalledTimes(2));
  } finally {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
    vi.restoreAllMocks();
  }
});
