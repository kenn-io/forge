import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import DaemonStatus from "./DaemonStatus.svelte";

type DaemonStoreStub = {
  isAvailable: () => boolean;
  getVersion: () => string;
  getActiveWorkers: () => number;
  getMaxWorkers: () => number;
  getQueuedJobs: () => number;
  getRunningJobs: () => number;
  getCompletedJobs: () => number;
  getFailedJobs: () => number;
  checkHealth: () => Promise<void>;
};

const state = {
  daemon: null as DaemonStoreStub | null,
};

vi.mock("../../context.js", () => ({
  getStores: () => ({
    roborevDaemon: state.daemon,
  }),
}));

function createDaemonStore(version: string): DaemonStoreStub {
  return {
    isAvailable: () => true,
    getVersion: () => version,
    getActiveWorkers: () => 1,
    getMaxWorkers: () => 4,
    getQueuedJobs: () => 2,
    getRunningJobs: () => 1,
    getCompletedJobs: () => 5,
    getFailedJobs: () => 0,
    checkHealth: vi.fn(async () => undefined),
  };
}

describe("DaemonStatus", () => {
  beforeEach(() => {
    state.daemon = createDaemonStore("v0.52.0");
  });

  afterEach(() => {
    cleanup();
    state.daemon = null;
  });

  it("does not prepend another v when the daemon version already has one", () => {
    render(DaemonStatus);

    expect(screen.getByTitle("Daemon version").textContent).toBe("v0.52.0");
  });

  it("prepends v when the daemon version is returned without one", () => {
    state.daemon = createDaemonStore("0.52.0");

    render(DaemonStatus);

    expect(screen.getByTitle("Daemon version").textContent).toBe("v0.52.0");
  });
});
