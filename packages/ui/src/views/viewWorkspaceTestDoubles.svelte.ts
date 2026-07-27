import { createRawSnippet, type Snippet } from "svelte";
import type { Attachment } from "svelte/attachments";
import { vi } from "vite-plus/test";
import {
  identityEquals,
  type PromotableSession,
  type InlineDockMode,
  type InlineWorkspaceController,
  type InlineWorkspaceSurface,
  type WorkspaceItemIdentity,
  type WorkspaceRefLite,
} from "../workspace-inline.js";

/**
 * Reactive `InlineWorkspaceController` test double for the view-level claim
 * lifecycle specs (PRListView/IssueListView/ActivityFeedView). Unlike
 * `inlineWorkspaceTestController` (which only needs `getDockMode`/`setDockMode`
 * backed by `$state`), these specs assert that a view's own
 * `available={... inlineWorkspace.isClaimedFor(claimIdentity)}` pane expression
 * reacts to `claim`/`release` calls the same way the real workspace-host store's
 * `isClaimedFor` (backed by `$state`) does — so `claim`/`release`/`isClaimedFor`
 * here are backed by real `$state` too. This module is a `.svelte.ts` file so
 * runes compile here.
 */
export function createClaimTestController(
  surface: InlineWorkspaceSurface = "prs",
  options: { sessions?: readonly PromotableSession[]; workspacePaneLabel?: string } = {},
): {
  controller: InlineWorkspaceController;
  notifyInvalidated: (identity: WorkspaceItemIdentity) => void;
  setSessions: (sessions: readonly PromotableSession[]) => void;
} {
  let claimed: WorkspaceItemIdentity | null = $state(null);
  let dockMode = $state<InlineDockMode>("split");
  let sessions = $state<readonly PromotableSession[]>(options.sessions ?? []);
  const invalidationListeners = new Set<(identity: WorkspaceItemIdentity) => void>();

  const controller: InlineWorkspaceController = {
    surface,
    effectiveWorkspaceRef: vi.fn(
      (_identity: WorkspaceItemIdentity, envelopeRef: WorkspaceRefLite | null | undefined) => envelopeRef ?? null,
    ),
    claim: vi.fn((identity: WorkspaceItemIdentity) => {
      claimed = identity;
    }),
    release: vi.fn(() => {
      claimed = null;
    }),
    isClaimedFor: vi.fn((identity: WorkspaceItemIdentity) => claimed !== null && identityEquals(claimed, identity)),
    recordCreated: vi.fn(),
    recordDeleted: vi.fn(),
    reconcile: vi.fn(),
    getDockMode: () => dockMode,
    setDockMode: vi.fn((next: InlineDockMode) => {
      dockMode = next;
    }),
    notePaneFocused: vi.fn(),
    focusTerminal: vi.fn(),
    openInWorkspaces: vi.fn(),
    onIdentityInvalidated: vi.fn((cb: (identity: WorkspaceItemIdentity) => void) => {
      invalidationListeners.add(cb);
      return () => invalidationListeners.delete(cb);
    }),
    slotAttachment: vi.fn((_element: HTMLElement) => {}) satisfies Attachment<HTMLElement>,
    promotableSessions: () => sessions,
    workspacePaneLabel: () => options.workspacePaneLabel ?? "Workspace",
    sessionPane: () => sessionPaneDouble,
  };

  return {
    controller,
    notifyInvalidated: (identity: WorkspaceItemIdentity) => {
      for (const listener of invalidationListeners) listener(identity);
    },
    setSessions: (next: readonly PromotableSession[]) => {
      sessions = next;
    },
  };
}

/**
 * Stand-in for the frontend's session-terminal slot, which packages/ui cannot
 * import. Records the pane key and the visibility the view passed through, which
 * is the whole contract: a pane that renders but reports itself hidden leaves a
 * live terminal off screen.
 */
const sessionPaneDouble: Snippet<[{ paneKey: string; visible: boolean }]> = createRawSnippet((arg) => ({
  render: () => `<div></div>`,
  setup: (node: Element) => {
    $effect(() => {
      const { paneKey, visible } = arg();
      node.setAttribute("data-session-pane", paneKey);
      node.setAttribute("data-session-pane-visible", String(visible));
    });
  },
}));

/** A `$state`-backed box so a test can mutate a store mock's return value and
 * have a component's `$effect` (which reads it) rerun, the same way a real
 * store's reactive state drives that component in production. */
export function createReactiveValue<T>(initial: T): { get: () => T; set: (next: T) => void } {
  let value = $state(initial);
  return {
    get: () => value,
    set: (next: T) => {
      value = next;
    },
  };
}
