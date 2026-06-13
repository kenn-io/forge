// Shared module-mock preset for App-level shell tests (App.*.test.ts).
//
// Importing this module registers vi.mock factories for everything the App
// shell pulls in that is invariant across those suites: chrome components
// (status bar, palettes, settings/design pages, terminal views), lazy mode
// features (kata/docs/messages with their testid-bearing mocks), and the
// data/api modules the shell touches on mount. Import it BEFORE App.svelte
// is imported (test files import it at the top, ahead of any dynamic
// `import("./App.svelte")`), so the mocks are registered first.
//
// Deliberately NOT in the preset, because suites diverge on them:
// - "@middleman/ui" (Provider/view-stub mapping and getStores overrides are
//   the per-suite configuration surface)
// - "./lib/components/layout/AppHeader.svelte" (some suites need the real
//   header, others a distinguishable stub)
// - "./lib/utils/appStartup.js" (see appShellInstantStartupMock.ts; the
//   startup-timeout suite needs the real implementation)
import { vi } from "vite-plus/test";

vi.mock("../components/layout/StatusBar.svelte", async () => ({
  default: (await import("./AppViewStub.svelte")).default,
}));
vi.mock("../components/keyboard/Palette.svelte", async () => ({
  default: (await import("./AppViewStub.svelte")).default,
}));
vi.mock("../components/keyboard/Cheatsheet.svelte", async () => ({
  default: (await import("./AppViewStub.svelte")).default,
}));
vi.mock("../components/repositories/RepoSummaryPage.svelte", async () => ({
  default: (await import("./AppViewStub.svelte")).default,
}));
vi.mock("../components/settings/SettingsPage.svelte", async () => ({
  default: (await import("./AppSettingsPageStub.svelte")).default,
}));
vi.mock("../components/terminal/WorkspaceTerminalView.svelte", async () => ({
  default: (await import("./AppViewStub.svelte")).default,
}));
vi.mock("../components/terminal/WorkspaceEmbedShell.svelte", async () => ({
  default: (await import("./AppViewStub.svelte")).default,
}));
vi.mock("../components/design-system/DesignSystemPage.svelte", async () => ({
  default: (await import("./AppViewStub.svelte")).default,
}));
vi.mock("../components/FlashBanner.svelte", async () => ({
  default: (await import("./AppViewStub.svelte")).default,
}));
vi.mock("../features/kata/KataFeature.svelte", async () => ({
  default: (await import("../features/kata/KataWorkspaceTestStub.svelte")).default,
}));
vi.mock("../features/docs/DocsFeature.svelte", async () => ({
  default: (await import("./AppDocsFeatureMock.svelte")).default,
}));
vi.mock("../features/messages/MessagesFeature.svelte", async () => ({
  default: (await import("./AppMessagesFeatureMock.svelte")).default,
}));

vi.mock("../api/kata/daemons.js", () => ({
  fetchKataDaemons: vi.fn(async () => []),
}));
vi.mock("../api/kata/taskClient.js", () => ({
  createKataTaskAPI: () => ({}),
}));
vi.mock("../api/docs/api.js", () => ({
  createDocsAPI: () => ({}),
}));
vi.mock("../api/messages/api.js", () => ({
  createMessagesAPI: () => ({
    capabilities: vi.fn(() =>
      Promise.resolve({
        configured: true,
        ok: true,
        features: {},
      }),
    ),
  }),
}));
vi.mock("../api/messages/visibility.js", () => ({
  shouldShowMessagesMode: () => true,
}));
vi.mock("../messages/kataMessageLinker.js", () => ({
  createMessageIssueLinker: () => ({
    linkMessage: vi.fn(),
  }),
}));

// RepoTypeahead (inside the real AppHeader) fetches repos on mount; suites
// that stub AppHeader never import this, so the mock is inert there.
vi.mock("../api/runtime.js", () => ({
  client: {
    GET: vi.fn(async () => ({ data: [], error: undefined })),
  },
  apiErrorMessage: () => "",
}));

// jsdom layout reports clientWidth 0, which the container observer would
// classify as "narrow" and swap in the compact/phone presentation. The
// converted Playwright specs all ran at a desktop viewport, so pin wide.
vi.mock("../stores/container.svelte.js", () => ({
  initContainerObserver: () => () => {},
  getContainerSize: () => "wide" as const,
  isNarrow: () => false,
}));

export function installBrowserGlobals(): void {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = vi.fn();
    },
  );
}

export function createAppTarget(): HTMLDivElement {
  const target = document.createElement("div");
  target.id = "app";
  document.body.appendChild(target);
  return target;
}
