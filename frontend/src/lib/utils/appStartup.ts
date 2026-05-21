import type { StoreInstances } from "@middleman/ui";
import type { Settings } from "@middleman/ui/api/types";

export interface AppStartupDeps {
  getSettings: () => Promise<Settings>;
  getStores: () => StoreInstances | undefined;
  onReady: () => void;
  beforeInitialLoad?: () => void;
}

const SETTINGS_STARTUP_TIMEOUT_MS = 8_000;

async function loadSettingsWithTimeout(
  getSettings: () => Promise<Settings>,
): Promise<Settings> {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      getSettings(),
      new Promise<Settings>((_, reject) => {
        timeout = setTimeout(() => {
          reject(
            new Error(
              "timed out loading settings during startup",
            ),
          );
        }, SETTINGS_STARTUP_TIMEOUT_MS);
      }),
    ]);
  } finally {
    if (timeout !== undefined) clearTimeout(timeout);
  }
}

/**
 * runAppStartup kicks off the async initialization work App.svelte
 * performs during onMount: fetching settings, hydrating store
 * defaults, and wiring live-update subscriptions once both have
 * resolved.
 *
 * It returns a cancel function that must be called from the
 * component's cleanup path. If cancellation fires before the
 * settings fetch resolves, no post-await side effects run, so
 * the component cannot leak an EventSource or start polling
 * after it has already unmounted.
 */
export function runAppStartup(deps: AppStartupDeps): () => void {
  let cancelled = false;
  void (async () => {
    try {
      const settings = await loadSettingsWithTimeout(
        deps.getSettings,
      );
      if (cancelled) return;
      const stores = deps.getStores();
      if (stores) {
        stores.settings.setConfiguredRepos(settings.repos);
        stores.settings.setTerminalFontFamily(
          settings.terminal.font_family,
        );
        stores.settings.setTerminalRenderer(settings.terminal.renderer);
        stores.activity.hydrateDefaults(settings.activity);
      }
    } catch (err) {
      if (cancelled) return;
      console.warn(
        "Failed to load settings, using defaults:",
        err,
      );
    }
    if (cancelled) return;
    deps.beforeInitialLoad?.();
    if (cancelled) return;
    deps.onReady();
    const stores = deps.getStores();
    if (stores) {
      stores.sync.startPolling();
      void stores.pulls.loadPulls();
      void stores.issues.loadIssues();
      stores.events.connect();
    }
  })();
  return () => {
    cancelled = true;
  };
}
