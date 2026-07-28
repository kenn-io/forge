import type { Attachment } from "svelte/attachments";
import { vi } from "vite-plus/test";
import type { InlineDockMode, InlineWorkspaceController, InlineWorkspaceSurface } from "../../workspace-inline.js";

/**
 * Minimal `InlineWorkspaceController` test double for the detail components that
 * merely thread a controller through (their workspace buttons, focus requests,
 * create/delete plumbing). `getDockMode`/`setDockMode` are backed by a real
 * `$state` cell — this module is a `.svelte.ts` file, so runes compile here — so
 * a consumer's own `$derived`/`$effect` blocks react to `setDockMode` the same
 * way they do against the real workspace-host store, with no `rerender()` needed
 * between interactions in one test.
 *
 * Prefer `views/viewWorkspaceTestDoubles.svelte.ts` when a spec needs claims to
 * be reactive as well.
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
    notePaneFocused: vi.fn(),
    focusTerminal: vi.fn(),
    openInWorkspaces: vi.fn(),
    onIdentityInvalidated: () => () => {},
    slotAttachment: vi.fn((_element: HTMLElement) => {}) satisfies Attachment<HTMLElement>,
    // These components thread the controller through without rendering panes.
    promotableSessions: () => [],
    workspacePaneLabel: () => "Workspace",
    workspacePaneEmpty: () => false,
    dockRow: () => null,
    sessionPane: () => null,
  };
}
