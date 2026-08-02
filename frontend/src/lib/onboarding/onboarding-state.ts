import type { Page } from "../stores/router.svelte.ts";

export type OnboardingState = "active" | "dismissed" | "complete";

const ONBOARDING_STORAGE_KEY = "kenn-forge:first-run-onboarding";
const onboardingEntryPages = new Set<Page>([
  "activity",
  "mobile-activity",
  "mobile-pulls",
  "mobile-issues",
  "pulls",
  "issues",
  "focus",
]);

export function shouldStartOnboarding(
  page: Page,
  hasConfiguredRepos: boolean,
  storedState: OnboardingState | null,
): boolean {
  if (!onboardingEntryPages.has(page)) return false;
  if (storedState === "dismissed") return false;
  if (!hasConfiguredRepos) return true;
  return storedState === "active";
}

export function readOnboardingState(): OnboardingState | null {
  try {
    const sessionValue = sessionStorage.getItem(ONBOARDING_STORAGE_KEY);
    if (sessionValue === "dismissed") return sessionValue;
    const value = localStorage.getItem(ONBOARDING_STORAGE_KEY);
    return value === "active" || value === "dismissed" || value === "complete" ? value : null;
  } catch {
    return null;
  }
}

export function writeOnboardingState(state: OnboardingState): void {
  try {
    if (state === "dismissed") {
      localStorage.removeItem(ONBOARDING_STORAGE_KEY);
      sessionStorage.setItem(ONBOARDING_STORAGE_KEY, state);
    } else {
      sessionStorage.removeItem(ONBOARDING_STORAGE_KEY);
      localStorage.setItem(ONBOARDING_STORAGE_KEY, state);
    }
  } catch {
    // Storage can be unavailable in private or embedded browser contexts.
    // The App-owned in-memory state still controls the current session.
  }
}
