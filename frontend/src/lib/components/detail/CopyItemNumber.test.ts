import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";

const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => {
    const runtime = runtimeCapture.current;
    if (runtime === undefined) throw new Error("copy item number test runtime is not initialized");
    return runtime;
  },
}));

import CopyItemNumber from "./CopyItemNumber.svelte";

describe("CopyItemNumber", () => {
  beforeEach(() => {
    runtimeCapture.current = makeAppRuntime();
    vi.useFakeTimers();
  });

  afterEach(async () => {
    cleanup();
    if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
    runtimeCapture.current = undefined;
    vi.useRealTimers();
  });

  it("does not retain feedback work when an in-flight clipboard write outlives the component", async () => {
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    let completeWrite = () => {};
    const pendingWrite = new Promise<void>((resolve) => {
      completeWrite = resolve;
    });
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn(() => pendingWrite) },
    });

    const view = render(CopyItemNumber, {
      props: { kind: "issue", number: 42, url: "https://example.test/issues/42" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Copy issue #42 link" }));
    view.unmount();
    const callsBeforeClipboardSettled = setTimeoutSpy.mock.calls.length;
    completeWrite();
    await Promise.resolve();
    await Promise.resolve();

    const retainedFeedbackTimer = setTimeoutSpy.mock.calls
      .slice(callsBeforeClipboardSettled)
      .some(([, delay]) => delay === 1_500);
    expect(retainedFeedbackTimer).toBe(false);
  });
});
