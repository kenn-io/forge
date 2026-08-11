import { beforeEach, describe, expect, it } from "vite-plus/test";

import { readOnboardingState, shouldStartOnboarding, writeOnboardingState } from "./onboarding-state.ts";

describe("first-run onboarding state", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  it("starts only on provider workflow entry routes", () => {
    expect(shouldStartOnboarding("activity", false, null)).toBe(true);
    expect(shouldStartOnboarding("pulls", false, null)).toBe(true);
    expect(shouldStartOnboarding("issues", false, null)).toBe(true);
    expect(shouldStartOnboarding("docs", false, null)).toBe(false);
    expect(shouldStartOnboarding("settings", false, null)).toBe(false);
    expect(shouldStartOnboarding("repos", false, null)).toBe(false);
    expect(shouldStartOnboarding("workspaces", false, "active")).toBe(false);
    expect(shouldStartOnboarding("terminal", false, "active")).toBe(false);
  });

  it("starts for an unfinished installation or an active provider-flow resume", () => {
    expect(shouldStartOnboarding("activity", true, null)).toBe(false);
    expect(shouldStartOnboarding("activity", true, "active")).toBe(true);
    expect(shouldStartOnboarding("activity", false, "dismissed")).toBe(false);
    expect(shouldStartOnboarding("activity", false, "complete")).toBe(true);
  });

  it("keeps dismissal session-scoped while persisting active and complete states", () => {
    writeOnboardingState("active");
    writeOnboardingState("dismissed");
    expect(readOnboardingState()).toBe("dismissed");
    sessionStorage.clear();
    expect(readOnboardingState()).toBe("active");

    writeOnboardingState("complete");
    expect(readOnboardingState()).toBe("complete");

    localStorage.setItem("kenn-forge:first-run-onboarding", "unknown");
    expect(readOnboardingState()).toBeNull();
  });
});
