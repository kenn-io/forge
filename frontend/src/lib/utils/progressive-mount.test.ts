import { describe, expect, it, vi } from "vite-plus/test";
import { createProgressiveMountController } from "./progressive-mount.js";

describe("createProgressiveMountController", () => {
  it("mounts bounded batches and reports completion", () => {
    const callbacks: Array<() => void> = [];
    const progress = vi.fn();
    const complete = vi.fn();
    const controller = createProgressiveMountController({
      batchSize: 25,
      schedule: (callback) => {
        callbacks.push(callback);
        return callbacks.length;
      },
      cancel: vi.fn(),
    });

    controller.start(50, 106, progress, complete);
    expect(callbacks).toHaveLength(1);

    callbacks.shift()?.();
    expect(progress).toHaveBeenLastCalledWith(75);
    callbacks.shift()?.();
    expect(progress).toHaveBeenLastCalledWith(100);
    callbacks.shift()?.();
    expect(progress).toHaveBeenLastCalledWith(106);
    expect(complete).toHaveBeenCalledOnce();
  });

  it("cancels pending work when a new mount starts", () => {
    const cancel = vi.fn();
    const controller = createProgressiveMountController({
      schedule: () => 41,
      cancel,
    });

    controller.start(0, 100, vi.fn());
    controller.start(0, 50, vi.fn());

    expect(cancel).toHaveBeenCalledWith(41);
  });
});
