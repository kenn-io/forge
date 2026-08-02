import { beforeEach, describe, expect, it } from "vite-plus/test";

import { readOnboardingState, shouldStartOnboarding, writeOnboardingState } from "./onboarding-state.ts";

describe("first-run onboarding state", () => {
  beforeEach(() => localStorage.clear());

  it("starts only for an unfinished installation or an active resume", () => {
    expect(shouldStartOnboarding(false, null)).toBe(true);
    expect(shouldStartOnboarding(true, null)).toBe(false);
    expect(shouldStartOnboarding(true, "active")).toBe(true);
    expect(shouldStartOnboarding(false, "dismissed")).toBe(false);
    expect(shouldStartOnboarding(false, "complete")).toBe(false);
  });

  it("persists recognized lifecycle states", () => {
    writeOnboardingState("active");
    expect(readOnboardingState()).toBe("active");

    localStorage.setItem("kenn-forge:first-run-onboarding", "unknown");
    expect(readOnboardingState()).toBeNull();
  });
});
