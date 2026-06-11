import { beforeEach, describe, expect, it } from "vite-plus/test";

import {
  getActiveKataDaemon,
  getDefaultKataDaemon,
  getKataDaemonRoster,
  getKataDaemonRosterLoaded,
  reconcileActiveKataDaemon,
  resetKataDaemonRoster,
  setActiveKataDaemon,
  setKataDaemonRoster,
} from "./active-kata-daemon.svelte.js";

describe("active kata daemon store", () => {
  beforeEach(() => {
    localStorage.clear();
    setActiveKataDaemon(undefined);
    resetKataDaemonRoster();
  });

  it("persists and returns the active daemon id", () => {
    setActiveKataDaemon("work");

    expect(getActiveKataDaemon()).toBe("work");
    expect(localStorage.getItem("middleman:kata:active_daemon")).toBe("work");
  });

  it("keeps a valid daemon selection during reconcile", () => {
    setActiveKataDaemon("work");

    reconcileActiveKataDaemon(["home", "work"], "home");

    expect(getActiveKataDaemon()).toBe("work");
  });

  it("resets a stale selection to the default daemon", () => {
    setActiveKataDaemon("gone");

    reconcileActiveKataDaemon(["home", "work"], "home");

    expect(getActiveKataDaemon()).toBe("home");
    expect(localStorage.getItem("middleman:kata:active_daemon")).toBe("home");
  });

  it("clears storage when the active daemon is unset", () => {
    setActiveKataDaemon("work");

    setActiveKataDaemon(undefined);

    expect(getActiveKataDaemon()).toBeUndefined();
    expect(localStorage.getItem("middleman:kata:active_daemon")).toBeNull();
  });

  it("stores the live daemon roster and default daemon", () => {
    setKataDaemonRoster(["home", "work"], "home");

    expect(getKataDaemonRoster()).toEqual(["home", "work"]);
    expect(getDefaultKataDaemon()).toBe("home");
    expect(getKataDaemonRosterLoaded()).toBe(true);
  });

  it("reconciles stale active daemon when the live roster changes", () => {
    setActiveKataDaemon("gone");

    setKataDaemonRoster(["home", "work"], "home");

    expect(getActiveKataDaemon()).toBe("home");
    expect(localStorage.getItem("middleman:kata:active_daemon")).toBe("home");
  });

  it("tracks whether the live daemon roster has resolved", () => {
    expect(getKataDaemonRosterLoaded()).toBe(false);

    setKataDaemonRoster([], undefined);

    expect(getKataDaemonRoster()).toEqual([]);
    expect(getKataDaemonRosterLoaded()).toBe(true);

    resetKataDaemonRoster();

    expect(getKataDaemonRoster()).toEqual([]);
    expect(getDefaultKataDaemon()).toBeUndefined();
    expect(getKataDaemonRosterLoaded()).toBe(false);
  });
});
