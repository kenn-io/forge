export type OnboardingState = "active" | "dismissed" | "complete";

const ONBOARDING_STORAGE_KEY = "kenn-forge:first-run-onboarding";

export function shouldStartOnboarding(hasConfiguredRepos: boolean, storedState: OnboardingState | null): boolean {
  if (storedState === "active") return true;
  if (storedState === "dismissed" || storedState === "complete") return false;
  return !hasConfiguredRepos;
}

export function readOnboardingState(): OnboardingState | null {
  try {
    const value = localStorage.getItem(ONBOARDING_STORAGE_KEY);
    return value === "active" || value === "dismissed" || value === "complete" ? value : null;
  } catch {
    return null;
  }
}

export function writeOnboardingState(state: OnboardingState): void {
  try {
    localStorage.setItem(ONBOARDING_STORAGE_KEY, state);
  } catch {
    // Storage can be unavailable in private or embedded browser contexts.
    // The App-owned in-memory state still controls the current session.
  }
}
