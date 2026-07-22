import type { Attachment } from "svelte/attachments";
import { vi } from "vite-plus/test";
import type { InlineDockMode, InlineWorkspaceController, InlineWorkspaceSurface } from "../../workspace-inline.js";

/**
 * Minimal `InlineWorkspaceController` test double for `WorkspaceDockPanel`
 * specs. `getDockMode`/`setDockMode` are backed by a real `$state` cell
 * (this module is a `.svelte.ts` file, so runes compile here) so the panel's
 * own `$derived`/`$effect` blocks react to `setDockMode` calls the same way
 * they would against the real frontend workspace-host store, without
 * needing a `rerender()` between interactions within a single test.
 */
export function createTestController(
  initialMode: InlineDockMode,
  surface: InlineWorkspaceSurface = "prs",
): InlineWorkspaceController {
  let mode = $state<InlineDockMode>(initialMode);

  return {
    surface,
    effectiveWorkspaceRef: () => null,
    claim: vi.fn(),
    release: vi.fn(),
    isClaimedFor: () => false,
    recordCreated: vi.fn(),
    recordDeleted: vi.fn(),
    reconcile: vi.fn(),
    getDockMode: () => mode,
    setDockMode: vi.fn((next: InlineDockMode) => {
      mode = next;
    }),
    focusTerminal: vi.fn(),
    openInWorkspaces: vi.fn(),
    onIdentityInvalidated: () => () => {},
    slotAttachment: vi.fn((_element: HTMLElement) => {}) satisfies Attachment<HTMLElement>,
  };
}
