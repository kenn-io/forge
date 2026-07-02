# Keybindings, Command Palette, and Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the unified keyboard-first command palette, `?` cheatsheet, and inline-hint surfaces specified in `docs/superpowers/specs/2026-05-07-keybindings-design.md`. v1 covers PRs, issues, and named commands; repos and workspaces are deferred per the spec's Out-of-scope.

**Architecture:** A frontend-only system. Visual primitive (`KbdBadge`) lands in the shared `@middleman/ui` package. App-specific orchestration (registry, dispatcher, palette, cheatsheet, action catalog) lands in `frontend/src/lib`. Existing `App.svelte` `handleKeydown` is migrated to a default catalog of named actions. Existing modal-like overlays are wired to push/pop modal frames so the dispatcher's modal-stack rule isolates them.

**Tech Stack:** Svelte 5 runes, TypeScript, Vitest (unit + component), `@testing-library/svelte`, Playwright (e2e), `@middleman/ui` shared package, `bun` for frontend tooling.

**Critical layering rule:** middleman has two frontend packages — `@middleman/ui` (`packages/ui/`) and the app shell (`frontend/src/lib/`). `@middleman/ui` cannot import upward from the app shell. Where this plan shows `packages/ui` modals (e.g. `MergeModal.svelte`, `ApproveButton.svelte`) needing access to `pushModalFrame` or PR-detail closures, those infrastructure pieces actually live in `@middleman/ui`, not in `frontend/src/lib`. Concretely:

- `pushModalFrame` and the modal-stack store live in `packages/ui/src/stores/keyboard/modal-stack.svelte.ts`. The `KeySpec` interface and a structurally-compatible `ModalFrameAction` interface live in `packages/ui/src/stores/keyboard/keyspec.ts`. Add a `./stores/keyboard/keyspec` and `./stores/keyboard/modal-stack` export to `packages/ui/package.json`.
- The PR-detail `canX/runX` closures live in `packages/ui/src/components/detail/keyboard-actions.ts`, alongside the buttons that consume them.
- `KbdBadge.svelte` and `useKbdLabel.ts` live in `packages/ui/src/components/keyboard/` and import only from `@middleman/ui/stores/keyboard/keyspec`.
- The full `Action`, `Context`, `ScopeTag`, `CheatsheetEntry`, and `RecentsState` types stay in `frontend/src/lib/stores/keyboard/types.ts`. The app-shell `Action` type is structurally assignable to `@middleman/ui`'s `ModalFrameAction`.
- The registry, dispatcher, default catalog, palette/cheatsheet components, recents, and palette/cheatsheet state stores stay in the app shell.
- Shared-package components that need to register cheatsheet entries (e.g. `CommentEditor.svelte`) call a host proxy in `packages/ui/src/stores/keyboard/cheatsheet-host.ts` that the app shell wires up at boot via `Provider.svelte` (mirroring how the existing context/action provider plumbing works).

When a task below shows an import like `import { pushModalFrame } from "../../../../frontend/src/lib/stores/keyboard/modal-stack.svelte.js"`, treat the path as wrong and substitute the in-package path (e.g. `from "../../stores/keyboard/modal-stack.svelte.js"` for a file inside `packages/ui`). Where the task shows `pr-detail-actions.ts` or any cross-package shared closure under `frontend/src/lib`, place the file under `packages/ui/src/components/detail/keyboard-actions.ts` instead.

---

## File Structure

**Package layering note:** middleman has two frontend packages — the shared `@middleman/ui` (`packages/ui/`) and the app shell (`frontend/src/lib/`). `@middleman/ui` cannot import upward from the app shell. Anything that the shared package's existing modals or buttons (MergeModal, ApproveButton, etc.) need access to must live in `@middleman/ui`. The split below respects that boundary.

**New files in `@middleman/ui`:**

- `packages/ui/src/components/keyboard/KbdBadge.svelte` — generic kbd glyph component (visible on pointer devices, hidden on touch).
- `packages/ui/src/components/keyboard/useKbdLabel.ts` — platform glyph helper (`⌘` on macOS, `Ctrl` elsewhere).
- `packages/ui/src/stores/keyboard/modal-stack.svelte.ts` — modal frame push/pop state. Lives here because `MergeModal`, `ApproveButton`, and other shared-package modals call `pushModalFrame` from inside `@middleman/ui`.
- `packages/ui/src/stores/keyboard/keyspec.ts` — `KeySpec` interface and a structurally-compatible `ModalFrameAction` interface used by the modal stack. The full `Action` type lives in the app shell and is structurally assignable to `ModalFrameAction`.
- `packages/ui/src/components/detail/keyboard-actions.ts` — extracted PR detail handlers and availability predicates shared between the existing detail buttons and the app-shell palette command registration.

**New files in the app shell:**

- `frontend/src/lib/stores/keyboard/registry.svelte.ts` — Svelte 5 rune store of action descriptors indexed by owner, plus a sibling cheatsheet-entries store.
- `frontend/src/lib/stores/keyboard/dispatch.svelte.ts` — single window-level keydown listener.
- `frontend/src/lib/stores/keyboard/context.svelte.ts` — derived `Context` object built from existing app stores.
- `frontend/src/lib/stores/keyboard/actions.ts` — v1 default action catalog (pure data + handler refs).
- `frontend/src/lib/stores/keyboard/recents.svelte.ts` — `localStorage`-backed recents store.
- `frontend/src/lib/stores/keyboard/types.ts` — shared app-shell types (`Action`, `ScopeTag`, `CheatsheetEntry`, `Context`, `RecentsState`). Re-exports `KeySpec` from `@middleman/ui/stores/keyboard/keyspec`.
- `frontend/src/lib/stores/keyboard/palette-state.svelte.ts` — palette open/close state with focus restore.
- `frontend/src/lib/stores/keyboard/cheatsheet-state.svelte.ts` — cheatsheet open/close state with focus restore.
- `frontend/src/lib/components/keyboard/Palette.svelte` — list+preview palette modal.
- `frontend/src/lib/components/keyboard/Cheatsheet.svelte` — read-only cheatsheet modal.
- Test files alongside each source file (`*.test.ts`, `*.svelte.test.ts`).

**Modified existing files:**

- `frontend/src/App.svelte` — `handleKeydown` is removed; default catalog is registered on mount; `Palette` and `Cheatsheet` components are rendered at the app root.
- `packages/ui/src/components/detail/MergeModal.svelte` — push/pop modal frame on open/close (imports from `../../stores/keyboard/modal-stack.svelte.js`).
- `packages/ui/src/components/detail/ApproveButton.svelte`, `ApproveWorkflowsButton.svelte`, `ReadyForReviewButton.svelte` — extract availability predicate and mutation handler into `packages/ui/src/components/detail/keyboard-actions.ts`; both the button and the app-shell palette command registration call the shared closure.
- `packages/ui/src/components/detail/IssueDetail.svelte` — push/pop modal frame on the embedded confirm sub-modal.
- `packages/ui/src/components/roborev/ShortcutHelpModal.svelte` — push/pop modal frame.
- `frontend/src/lib/components/repositories/RepoIssueModal.svelte` — push/pop modal frame.
- `frontend/src/lib/components/settings/RepoImportModal.svelte` — push/pop modal frame.
- `frontend/src/lib/components/RepoTypeahead.svelte` — call `registerCheatsheetEntries` on mount for its arrow-nav shortcuts.
- `packages/ui/src/components/detail/CommentEditor.svelte` — call `registerCheatsheetEntries` on mount for any comment editor shortcuts already present.
- `frontend/src/lib/components/layout/AppHeader.svelte` — render `KbdBadge` next to Sync, theme toggle, and sidebar toggle buttons.

**Note on `registerCheatsheetEntries` in `@middleman/ui` components:** Components in `@middleman/ui` (e.g. `CommentEditor.svelte`) that need to register cheatsheet entries call a thin proxy in `@middleman/ui` that forwards to the app-shell registry. The proxy is initialized at app boot with the real registry callback (similar to how `Provider.svelte` already wires shared-package context). The proxy lives in `packages/ui/src/stores/keyboard/cheatsheet-host.ts`.

---

## Task 1: Shared types

**Files:**
- Create: `frontend/src/lib/stores/keyboard/types.ts`

- [ ] **Step 1: Write the types file**

```ts
import type { Route, DetailTab } from "../router.svelte.js";
import type { PullSelection } from "@middleman/ui/stores/pulls";
import type { IssueSelection } from "@middleman/ui/stores/issues";
import type { RoutedItemRef } from "@middleman/ui/routes";

export type ScopeTag =
  | "global"
  | "view-pulls" | "view-issues"
  | "detail-pr" | "detail-issue";

export interface KeySpec {
  key: string;
  ctrlOrMeta?: boolean;
  shift?: boolean;
  alt?: boolean;
}

export interface Context {
  page: ReturnType<typeof import("../router.svelte.js").getPage>;
  route: Route;
  selectedPR: PullSelection | null;
  selectedIssue: IssueSelection | null;
  isDiffView: boolean;
  detailTab: DetailTab;
}

export interface PreviewBlock {
  title: string;
  subtitle?: string;
  body?: string;
  badge?: string;
}

export interface Action {
  id: string;
  label: string;
  scope: ScopeTag;
  binding: KeySpec | KeySpec[] | null;
  priority: number;
  when: (ctx: Context) => boolean;
  handler: (ctx: Context) => void | Promise<void>;
  preview?: (ctx: Context) => PreviewBlock;
}

export interface CheatsheetEntry {
  id: string;
  label: string;
  binding: KeySpec | KeySpec[];
  scope: ScopeTag;
  conditionBadge?: string;
}

export interface RecentsState {
  version: 1;
  items: Array<{
    kind: "pr" | "issue";
    ref: RoutedItemRef;
    lastSelectedAt: string;
  }>;
}
```

- [ ] **Step 2: Verify it type-checks**

Run: `cd frontend && bun run typecheck`
Expected: no errors related to `keyboard/types.ts`.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/stores/keyboard/types.ts
git commit -m "feat(keyboard): add shared action/context/recents types"
```

---

## Task 2: Registry store

**Files:**
- Create: `frontend/src/lib/stores/keyboard/registry.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/registry.svelte.test.ts`

- [ ] **Step 1: Write the failing tests**

```ts
import { beforeEach, describe, expect, it } from "vitest";

import {
  registerScopedActions,
  getActionsByOwner,
  getAllActions,
  registerCheatsheetEntries,
  getAllCheatsheetEntries,
  resetRegistry,
} from "./registry.svelte.js";
import type { Action, CheatsheetEntry } from "./types.js";

const trueWhen = () => true;
const noop = () => {};

const action = (id: string, scope: Action["scope"] = "global"): Action => ({
  id,
  label: id,
  scope,
  binding: null,
  priority: 0,
  when: trueWhen,
  handler: noop,
});

describe("registry", () => {
  beforeEach(() => resetRegistry());

  it("returns registered actions for an owner", () => {
    registerScopedActions("owner-a", [action("a.one"), action("a.two")]);
    expect(getActionsByOwner("owner-a")).toHaveLength(2);
  });

  it("cleanup removes only the owner's actions", () => {
    registerScopedActions("owner-a", [action("a.one")]);
    const cleanup = registerScopedActions("owner-b", [action("b.one")]);
    cleanup();
    expect(getActionsByOwner("owner-a")).toHaveLength(1);
    expect(getActionsByOwner("owner-b")).toHaveLength(0);
  });

  it("re-registering an owner replaces only its entries", () => {
    registerScopedActions("owner-a", [action("a.one")]);
    registerScopedActions("owner-b", [action("b.one")]);
    registerScopedActions("owner-a", [action("a.two")]);
    expect(getActionsByOwner("owner-a").map((a) => a.id)).toEqual(["a.two"]);
    expect(getActionsByOwner("owner-b").map((a) => a.id)).toEqual(["b.one"]);
  });

  it("getAllActions flattens entries across owners", () => {
    registerScopedActions("owner-a", [action("a.one")]);
    registerScopedActions("owner-b", [action("b.one")]);
    expect(getAllActions().map((a) => a.id).sort()).toEqual(["a.one", "b.one"]);
  });

  it("registerCheatsheetEntries supports owner-based replacement", () => {
    const entry = (id: string): CheatsheetEntry => ({
      id,
      label: id,
      binding: { key: id },
      scope: "view-pulls",
    });
    registerCheatsheetEntries("ce-a", [entry("a")]);
    registerCheatsheetEntries("ce-b", [entry("b")]);
    expect(getAllCheatsheetEntries().map((e) => e.id).sort()).toEqual(["a", "b"]);
    registerCheatsheetEntries("ce-a", []);
    expect(getAllCheatsheetEntries().map((e) => e.id)).toEqual(["b"]);
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd frontend && bun run test src/lib/stores/keyboard/registry.svelte.test.ts`
Expected: FAIL ("Cannot find module './registry.svelte.js'").

- [ ] **Step 3: Implement the registry**

```ts
import type { Action, CheatsheetEntry } from "./types.js";

let actionsByOwner = $state<Map<string, Action[]>>(new Map());
let cheatsheetByOwner = $state<Map<string, CheatsheetEntry[]>>(new Map());

export function registerScopedActions(
  ownerId: string,
  actions: Action[],
): () => void {
  actionsByOwner = new Map(actionsByOwner.set(ownerId, [...actions]));
  return () => {
    if (actionsByOwner.has(ownerId)) {
      const next = new Map(actionsByOwner);
      next.delete(ownerId);
      actionsByOwner = next;
    }
  };
}

export function registerCheatsheetEntries(
  ownerId: string,
  entries: CheatsheetEntry[],
): () => void {
  cheatsheetByOwner = new Map(cheatsheetByOwner.set(ownerId, [...entries]));
  return () => {
    if (cheatsheetByOwner.has(ownerId)) {
      const next = new Map(cheatsheetByOwner);
      next.delete(ownerId);
      cheatsheetByOwner = next;
    }
  };
}

export function getActionsByOwner(ownerId: string): Action[] {
  return actionsByOwner.get(ownerId) ?? [];
}

export function getAllActions(): Action[] {
  const out: Action[] = [];
  for (const list of actionsByOwner.values()) out.push(...list);
  return out;
}

export function getAllCheatsheetEntries(): CheatsheetEntry[] {
  const out: CheatsheetEntry[] = [];
  for (const list of cheatsheetByOwner.values()) out.push(...list);
  return out;
}

export function resetRegistry(): void {
  actionsByOwner = new Map();
  cheatsheetByOwner = new Map();
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/registry.svelte.test.ts`
Expected: all 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/registry.svelte.ts frontend/src/lib/stores/keyboard/registry.svelte.test.ts
git commit -m "feat(keyboard): add owner-scoped action and cheatsheet registry"
```

---

## Task 3: Conflict assertion

**Files:**
- Modify: `frontend/src/lib/stores/keyboard/registry.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/registry.svelte.test.ts`

- [ ] **Step 1: Add failing test**

Append to the existing test file:

```ts
describe("conflict assertion", () => {
  beforeEach(() => resetRegistry());

  it("throws when two actions share (binding, scope, priority)", () => {
    const collide: Action[] = [
      { ...action("a.one"), binding: { key: "k", ctrlOrMeta: true } },
    ];
    const collide2: Action[] = [
      { ...action("a.two"), binding: { key: "k", ctrlOrMeta: true } },
    ];
    registerScopedActions("o1", collide);
    expect(() => registerScopedActions("o2", collide2)).toThrow(
      /conflict/i,
    );
  });

  it("allows different scopes with the same binding", () => {
    registerScopedActions("o1", [
      { ...action("a", "view-pulls"), binding: { key: "j" } },
    ]);
    registerScopedActions("o2", [
      { ...action("b", "view-issues"), binding: { key: "j" } },
    ]);
    expect(getAllActions()).toHaveLength(2);
  });
});
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `cd frontend && bun run test src/lib/stores/keyboard/registry.svelte.test.ts`
Expected: 2 new tests FAIL.

- [ ] **Step 3: Add conflict assertion to `registerScopedActions`**

In `registry.svelte.ts`, replace the body of `registerScopedActions` with:

```ts
export function registerScopedActions(
  ownerId: string,
  actions: Action[],
): () => void {
  const next = new Map(actionsByOwner);
  next.set(ownerId, [...actions]);
  assertNoConflicts(next);
  actionsByOwner = next;
  return () => {
    if (actionsByOwner.has(ownerId)) {
      const cleanupNext = new Map(actionsByOwner);
      cleanupNext.delete(ownerId);
      actionsByOwner = cleanupNext;
    }
  };
}

function assertNoConflicts(map: Map<string, Action[]>): void {
  const seen = new Map<string, { id: string; owner: string }>();
  for (const [owner, list] of map) {
    for (const action of list) {
      if (action.binding === null) continue;
      const bindings = Array.isArray(action.binding) ? action.binding : [action.binding];
      for (const b of bindings) {
        const key = `${b.key}|${b.ctrlOrMeta ?? false}|${b.shift ?? false}|${b.alt ?? false}|${action.scope}|${action.priority}`;
        const prior = seen.get(key);
        if (prior) {
          const msg =
            `keyboard registry conflict: action '${prior.id}' (owner '${prior.owner}') ` +
            `and '${action.id}' (owner '${owner}') share binding+scope+priority`;
          if (import.meta.env?.DEV || import.meta.env?.MODE === "test") {
            throw new Error(msg);
          }
          // eslint-disable-next-line no-console
          console.warn(msg);
        } else {
          seen.set(key, { id: action.id, owner });
        }
      }
    }
  }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/registry.svelte.test.ts`
Expected: all tests PASS, including the two new conflict ones.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/registry.svelte.ts frontend/src/lib/stores/keyboard/registry.svelte.test.ts
git commit -m "feat(keyboard): assert no duplicate (binding, scope, priority) tuples"
```

---

## Task 4: Modal stack store

**Files:**
- Create: `frontend/src/lib/stores/keyboard/modal-stack.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/modal-stack.svelte.test.ts`

- [ ] **Step 1: Write failing tests**

```ts
import { beforeEach, describe, expect, it } from "vitest";

import {
  pushModalFrame,
  getTopFrame,
  getStackDepth,
  resetModalStack,
} from "./modal-stack.svelte.js";

describe("modal stack", () => {
  beforeEach(() => resetModalStack());

  it("starts empty", () => {
    expect(getStackDepth()).toBe(0);
    expect(getTopFrame()).toBeNull();
  });

  it("push then pop", () => {
    const pop = pushModalFrame("palette", []);
    expect(getStackDepth()).toBe(1);
    expect(getTopFrame()?.frameId).toBe("palette");
    pop();
    expect(getStackDepth()).toBe(0);
  });

  it("nested frames return topmost first", () => {
    const popA = pushModalFrame("a", []);
    pushModalFrame("b", []);
    expect(getTopFrame()?.frameId).toBe("b");
    popA(); // out-of-order pop is allowed; the frame is removed wherever it sits
    expect(getStackDepth()).toBe(1);
    expect(getTopFrame()?.frameId).toBe("b");
  });
});
```

- [ ] **Step 2: Run tests to verify failure**

Run: `cd frontend && bun run test src/lib/stores/keyboard/modal-stack.svelte.test.ts`
Expected: FAIL ("Cannot find module").

- [ ] **Step 3: Implement modal stack**

```ts
import type { Action } from "./types.js";

interface ModalFrame {
  frameId: string;
  actions: Action[];
}

let stack = $state<ModalFrame[]>([]);

export function pushModalFrame(
  frameId: string,
  actions: Action[],
): () => void {
  stack = [...stack, { frameId, actions }];
  return () => {
    stack = stack.filter((f) => f.frameId !== frameId);
  };
}

export function getTopFrame(): ModalFrame | null {
  return stack.length > 0 ? stack[stack.length - 1]! : null;
}

export function getStackDepth(): number {
  return stack.length;
}

export function getStack(): ModalFrame[] {
  return stack;
}

export function resetModalStack(): void {
  stack = [];
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/modal-stack.svelte.test.ts`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/modal-stack.svelte.ts frontend/src/lib/stores/keyboard/modal-stack.svelte.test.ts
git commit -m "feat(keyboard): add input-capturing modal frame stack"
```

---

## Task 5: Context object

**Files:**
- Create: `frontend/src/lib/stores/keyboard/context.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/context.svelte.test.ts`

- [ ] **Step 1: Write failing test**

```ts
import { describe, expect, it } from "vitest";

import { buildContext } from "./context.svelte.js";

describe("buildContext", () => {
  it("returns the current page, route, and detail-tab snapshot", () => {
    const ctx = buildContext({
      pulls: { getSelectedPR: () => null },
      issues: { getSelectedIssue: () => null },
    });
    expect(ctx).toHaveProperty("page");
    expect(ctx).toHaveProperty("route");
    expect(ctx).toHaveProperty("selectedPR", null);
    expect(ctx).toHaveProperty("selectedIssue", null);
    expect(ctx).toHaveProperty("isDiffView");
    expect(ctx).toHaveProperty("detailTab");
  });
});
```

- [ ] **Step 2: Run test to verify failure**

Run: `cd frontend && bun run test src/lib/stores/keyboard/context.svelte.test.ts`
Expected: FAIL.

- [ ] **Step 3: Implement context**

```ts
import {
  getRoute,
  getPage,
  getDetailTab,
  isDiffView,
} from "../router.svelte.js";
import type { Context } from "./types.js";
import type { PullSelection } from "@middleman/ui/stores/pulls";
import type { IssueSelection } from "@middleman/ui/stores/issues";

interface SelectionSources {
  pulls: { getSelectedPR: () => PullSelection | null };
  issues: { getSelectedIssue: () => IssueSelection | null };
}

export function buildContext(stores: SelectionSources): Context {
  return {
    page: getPage(),
    route: getRoute(),
    selectedPR: stores.pulls.getSelectedPR(),
    selectedIssue: stores.issues.getSelectedIssue(),
    isDiffView: isDiffView(),
    detailTab: getDetailTab(),
  };
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/context.svelte.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/context.svelte.ts frontend/src/lib/stores/keyboard/context.svelte.test.ts
git commit -m "feat(keyboard): build dispatch context from existing stores"
```

---

## Task 6: Dispatcher — basic match and conflict-free dispatch

**Files:**
- Create: `frontend/src/lib/stores/keyboard/dispatch.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts`

- [ ] **Step 1: Write failing tests**

```ts
import { beforeEach, describe, expect, it, vi } from "vitest";

import { dispatchKeydown } from "./dispatch.svelte.js";
import {
  registerScopedActions,
  resetRegistry,
} from "./registry.svelte.js";
import { resetModalStack } from "./modal-stack.svelte.js";
import type { Action, Context } from "./types.js";

const ctx: Context = {
  page: "pulls",
  route: { page: "pulls" } as never,
  selectedPR: null,
  selectedIssue: null,
  isDiffView: false,
  detailTab: "conversation",
};

const event = (init: Partial<KeyboardEvent>) =>
  Object.assign(new KeyboardEvent("keydown", init), {
    preventDefault: vi.fn(),
  });

describe("dispatchKeydown — global registry", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
  });

  it("runs the matching action's handler and preventDefaults", () => {
    const handler = vi.fn();
    const a: Action = {
      id: "go.next",
      label: "Next",
      scope: "view-pulls",
      binding: { key: "j" },
      priority: 0,
      when: () => true,
      handler,
    };
    registerScopedActions("test", [a]);
    const e = event({ key: "j" });
    dispatchKeydown(e, () => ctx);
    expect(handler).toHaveBeenCalled();
    expect(e.preventDefault).toHaveBeenCalled();
  });

  it("does not run actions whose when returns false", () => {
    const handler = vi.fn();
    const a: Action = {
      id: "go.next",
      label: "Next",
      scope: "view-pulls",
      binding: { key: "j" },
      priority: 0,
      when: () => false,
      handler,
    };
    registerScopedActions("test", [a]);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(handler).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Verify failure**

Run: `cd frontend && bun run test src/lib/stores/keyboard/dispatch.svelte.test.ts`
Expected: FAIL ("Cannot find module").

- [ ] **Step 3: Implement minimal dispatcher**

```ts
import { getStack } from "./modal-stack.svelte.js";
import { getAllActions } from "./registry.svelte.js";
import { shouldIgnoreGlobalShortcutTarget } from "../../utils/keyboardShortcuts.js";
import type { Action, Context, KeySpec } from "./types.js";

const RESERVED_WHILE_MODAL_OPEN: KeySpec[] = [
  { key: "k", ctrlOrMeta: true },
  { key: "p", ctrlOrMeta: true },
];

const SCOPE_SPECIFICITY: Record<Action["scope"], number> = {
  "detail-pr": 30,
  "detail-issue": 30,
  "view-pulls": 20,
  "view-issues": 20,
  global: 10,
};

export function dispatchKeydown(
  event: KeyboardEvent,
  contextProvider: () => Context,
): void {
  const stack = getStack();
  if (stack.length > 0) {
    for (let i = stack.length - 1; i >= 0; i--) {
      const frame = stack[i]!;
      for (const a of frame.actions) {
        if (matches(a.binding, event)) {
          event.preventDefault();
          void a.handler(contextProvider());
          return;
        }
      }
    }
    if (RESERVED_WHILE_MODAL_OPEN.some((b) => matches(b, event))) {
      event.preventDefault();
    }
    return;
  }

  const editable = shouldIgnoreGlobalShortcutTarget(event.target);
  const ctx = contextProvider();
  const matchingActions = getAllActions().filter(
    (a) =>
      a.binding !== null &&
      matches(a.binding, event) &&
      a.when(ctx) &&
      (!editable || hasModifier(a.binding)),
  );
  if (matchingActions.length === 0) return;

  matchingActions.sort((a, b) => {
    const sd = SCOPE_SPECIFICITY[b.scope] - SCOPE_SPECIFICITY[a.scope];
    if (sd !== 0) return sd;
    return b.priority - a.priority;
  });

  event.preventDefault();
  void matchingActions[0]!.handler(ctx);
}

function matches(spec: Action["binding"] | KeySpec, event: KeyboardEvent): boolean {
  if (spec === null) return false;
  const specs = Array.isArray(spec) ? spec : [spec];
  return specs.some((s) => {
    if (s.key.toLowerCase() !== event.key.toLowerCase()) return false;
    const meta = event.ctrlKey || event.metaKey;
    if ((s.ctrlOrMeta ?? false) !== meta) return false;
    if ((s.shift ?? false) !== event.shiftKey) return false;
    if ((s.alt ?? false) !== event.altKey) return false;
    return true;
  });
}

function hasModifier(spec: KeySpec | KeySpec[]): boolean {
  const specs = Array.isArray(spec) ? spec : [spec];
  return specs.some((s) => s.ctrlOrMeta || s.alt);
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/dispatch.svelte.test.ts`
Expected: 2 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/dispatch.svelte.ts frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts
git commit -m "feat(keyboard): single window-level dispatcher with scope sort"
```

---

## Task 7: Dispatcher — modal stack isolation, reserved keys, error handling

**Files:**
- Modify: `frontend/src/lib/stores/keyboard/dispatch.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts`

- [ ] **Step 1: Add failing tests for modal isolation, reserved keys, async errors**

Append to `dispatch.svelte.test.ts`:

```ts
import { pushModalFrame } from "./modal-stack.svelte.js";
import { showFlash } from "../flash.svelte.js";
import * as flashModule from "../flash.svelte.js";

describe("dispatchKeydown — modal stack", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
  });

  it("blocks global handlers when modal stack is non-empty", () => {
    const globalHandler = vi.fn();
    registerScopedActions("g", [
      { id: "g.next", label: "x", scope: "view-pulls", binding: { key: "j" }, priority: 0, when: () => true, handler: globalHandler },
    ]);
    pushModalFrame("modal", []);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(globalHandler).not.toHaveBeenCalled();
  });

  it("preventDefaults reserved keys (Cmd+K) when no frame action matches", () => {
    pushModalFrame("modal", []);
    const e = event({ key: "k", metaKey: true });
    dispatchKeydown(e, () => ctx);
    expect(e.preventDefault).toHaveBeenCalled();
  });

  it("does NOT preventDefault unmatched non-reserved keys", () => {
    pushModalFrame("modal", []);
    const e = event({ key: "x" });
    dispatchKeydown(e, () => ctx);
    expect(e.preventDefault).not.toHaveBeenCalled();
  });
});

describe("dispatchKeydown — error handling", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
  });

  it("routes async handler rejections to flash with the Error message", async () => {
    const flash = vi.spyOn(flashModule, "showFlash").mockImplementation(() => {});
    registerScopedActions("e", [
      {
        id: "fail",
        label: "Fail",
        scope: "global",
        binding: { key: "j" },
        priority: 0,
        when: () => true,
        handler: () => Promise.reject(new Error("boom")),
      },
    ]);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    await new Promise((r) => setTimeout(r, 0));
    expect(flash).toHaveBeenCalledWith(expect.stringContaining("boom"));
    flash.mockRestore();
  });
});
```

- [ ] **Step 2: Run tests to verify failures**

Run: `cd frontend && bun run test src/lib/stores/keyboard/dispatch.svelte.test.ts`
Expected: 4 new tests FAIL.

- [ ] **Step 3: Add error routing to dispatch**

In `dispatch.svelte.ts`, replace the action invocation lines (`void a.handler(...)`) with a wrapper:

```ts
import { showFlash } from "../flash.svelte.js";

function runHandler(action: Action, ctx: Context): void {
  try {
    const result = action.handler(ctx);
    if (result && typeof (result as Promise<void>).catch === "function") {
      (result as Promise<void>).catch((err: unknown) => surfaceError(action.id, err));
    }
  } catch (err) {
    surfaceError(action.id, err);
  }
}

function surfaceError(actionId: string, err: unknown): void {
  const msg = err instanceof Error && err.message ? err.message : "Command failed";
  if (!(err instanceof Error) || !err.message) {
    // eslint-disable-next-line no-console
    console.error(`keyboard action ${actionId} failed`, err);
  }
  showFlash(msg);
}
```

Replace the two `void a.handler(...)` / `void matchingActions[0]!.handler(...)` lines with `runHandler(a, contextProvider())` and `runHandler(matchingActions[0]!, ctx)` respectively.

- [ ] **Step 4: Run tests to verify pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/dispatch.svelte.test.ts`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/dispatch.svelte.ts frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts
git commit -m "feat(keyboard): isolate modal stack, route handler errors to flash"
```

---

## Task 8: In-flight de-duplication

**Files:**
- Modify: `frontend/src/lib/stores/keyboard/dispatch.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts`

- [ ] **Step 1: Write failing test**

```ts
describe("dispatchKeydown — in-flight de-dup", () => {
  beforeEach(() => {
    resetRegistry();
    resetModalStack();
  });

  it("does not re-invoke an in-flight async action", async () => {
    let resolve!: () => void;
    const handler = vi.fn(() => new Promise<void>((r) => { resolve = r; }));
    registerScopedActions("a", [
      { id: "slow", label: "x", scope: "global", binding: { key: "j" }, priority: 0, when: () => true, handler },
    ]);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(handler).toHaveBeenCalledTimes(1);
    resolve();
    await new Promise((r) => setTimeout(r, 0));
    dispatchKeydown(event({ key: "j" }), () => ctx);
    expect(handler).toHaveBeenCalledTimes(2);
  });
});
```

- [ ] **Step 2: Verify failure**

Run: `cd frontend && bun run test src/lib/stores/keyboard/dispatch.svelte.test.ts`
Expected: this test FAILS (handler called 2 times instead of 1).

- [ ] **Step 3: Add in-flight tracking**

In `dispatch.svelte.ts`:

```ts
const inFlight = new Set<string>();

function runHandler(action: Action, ctx: Context): void {
  if (inFlight.has(action.id)) return;
  try {
    const result = action.handler(ctx);
    if (result && typeof (result as Promise<void>).then === "function") {
      inFlight.add(action.id);
      (result as Promise<void>)
        .catch((err: unknown) => surfaceError(action.id, err))
        .finally(() => inFlight.delete(action.id));
    }
  } catch (err) {
    surfaceError(action.id, err);
  }
}
```

- [ ] **Step 4: Verify pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/dispatch.svelte.test.ts`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/dispatch.svelte.ts frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts
git commit -m "feat(keyboard): suppress concurrent invocations of in-flight actions"
```

---

## Task 9: Default action catalog

**Files:**
- Create: `frontend/src/lib/stores/keyboard/actions.ts`
- Test: `frontend/src/lib/stores/keyboard/actions.test.ts`
- Modify: `frontend/src/lib/stores/keyboard/types.ts` (add `StoreInstances` import re-export so the catalog can call store methods)

**Migration parity matrix (transcribed from current `frontend/src/App.svelte` `handleKeydown`):**

| Action id            | Binding                   | Scope         | when (predicate)                                            | Handler                              |
|----------------------|---------------------------|---------------|-------------------------------------------------------------|--------------------------------------|
| `go.next`            | `j`                       | `view-pulls`  | `page === "pulls"` && !isDiffView && !boardView && !isIssues | `pulls.selectNextPR(); navigateToSelectedPR()` |
| `go.prev`            | `k`                       | `view-pulls`  | same as `go.next`                                           | `pulls.selectPrevPR(); navigateToSelectedPR()` |
| `go.next.issues`     | `j`                       | `view-issues` | `page === "issues"`                                         | `issues.selectNextIssue()`           |
| `go.prev.issues`     | `k`                       | `view-issues` | `page === "issues"`                                         | `issues.selectPrevIssue()`           |
| `tab.toggle`         | `f`                       | `view-pulls`  | `page === "pulls"` && PR selected                           | navigate between conversation/files routes |
| `escape.list`        | `Escape`                  | `view-pulls`  | drawer open or detail open and not boardView                | `navigate("/pulls")` (or `/issues`)  |
| `nav.pulls.list`     | `1`                       | `global`      | always                                                       | `navigate("/pulls")`                 |
| `nav.pulls.board`    | `2`                       | `global`      | always                                                       | `navigate("/pulls/board")`           |
| `sidebar.toggle`     | `Cmd/Ctrl+[` (`ctrlOrMeta=true`) | `global` | `isSidebarToggleEnabled()`                                  | `toggleSidebar()`                     |
| `palette.open`       | `Cmd/Ctrl+K` AND `Cmd/Ctrl+P` | `global` | always                                                       | `openPalette()` (defined in palette-state from Task 17) |
| `cheatsheet.open`    | `?`                       | `global`      | always                                                       | `openCheatsheet()` (Task 24)         |
| `sync.repos`         | `null` (palette-only)     | `global`      | always                                                       | `stores.sync.triggerSync()`          |
| `theme.toggle`       | `null` (palette-only)     | `global`      | always                                                       | `toggleTheme()`                       |
| `nav.settings`       | `null`                    | `global`      | always                                                       | `navigate("/settings")`              |
| `nav.repos`          | `null`                    | `global`      | always                                                       | `navigate("/repos")`                 |
| `nav.reviews`        | `null`                    | `global`      | always                                                       | `navigate("/reviews")`               |
| `nav.workspaces`     | `null`                    | `global`      | always                                                       | `navigate("/workspaces")`            |
| `nav.design-system`  | `null`                    | `global`      | always                                                       | `navigate("/design-system")`         |

This matrix is the parity contract: every row's behavior must match the pre-migration `handleKeydown` exactly. Stage 2 e2e covers each row that has a binding.

**`StoreInstances` injection.** `actions.ts` does NOT import the app stores at module load. Instead, it calls a `getStores(): StoreInstances` getter that the app shell wires up at boot. Define the getter in `actions.ts`:

```ts
import type { StoreInstances } from "@middleman/ui";

let storesGetter: (() => StoreInstances) | null = null;
export function setStoreInstances(getter: () => StoreInstances): void { storesGetter = getter; }
function stores(): StoreInstances {
  if (!storesGetter) throw new Error("setStoreInstances has not been called");
  return storesGetter();
}
```

`palette-state.svelte.ts` (Task 17) and `cheatsheet-state.svelte.ts` (Task 24) export `openPalette`/`openCheatsheet` etc.; the catalog imports them directly. The default catalog file lands in stage 2 with the bindings listed above; the palette/cheatsheet handler stubs throw `new Error("not yet wired")` until those stages add the state stores. Stage 6 / 9 replace the stubs.

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from "vitest";

import { defaultActions } from "./actions.js";

describe("defaultActions", () => {
  it("includes the migrated globals", () => {
    const ids = defaultActions.map((a) => a.id);
    expect(ids).toEqual(
      expect.arrayContaining([
        "go.next",
        "go.prev",
        "tab.toggle",
        "escape.list",
        "nav.pulls.list",
        "nav.pulls.board",
        "sidebar.toggle",
        "palette.open",
        "cheatsheet.open",
        "sync.repos",
        "theme.toggle",
        "nav.settings",
        "nav.repos",
        "nav.reviews",
        "nav.workspaces",
        "nav.design-system",
      ]),
    );
  });

  it("palette.open binds Cmd/Ctrl+K and Cmd/Ctrl+P", () => {
    const palette = defaultActions.find((a) => a.id === "palette.open");
    expect(palette).toBeDefined();
    expect(palette!.binding).toEqual([
      { key: "k", ctrlOrMeta: true },
      { key: "p", ctrlOrMeta: true },
    ]);
  });
});
```

- [ ] **Step 2: Verify failure**

Run: `cd frontend && bun run test src/lib/stores/keyboard/actions.test.ts`
Expected: FAIL.

- [ ] **Step 3: Implement the catalog**

```ts
import { navigate } from "../router.svelte.js";
import { toggleSidebar, isSidebarToggleEnabled } from "../sidebar.svelte.js";
import { toggleTheme } from "../theme.svelte.js";
import { openPalette } from "./palette-state.svelte.js";
import { openCheatsheet } from "./cheatsheet-state.svelte.js";
import type { Action, Context } from "./types.js";
import type { StoreInstances } from "@middleman/ui";
// setStoreInstances + stores() are defined just above per the StoreInstances injection block.

const always = () => true;
const onPullsList = (ctx: Context) =>
  ctx.page === "pulls" && !ctx.isDiffView; // board view filter applied per-action below
const onPullsListNotBoard = (ctx: Context) =>
  onPullsList(ctx) && !("view" in ctx.route && (ctx.route as { view?: string }).view === "board");
const onIssuesList = (ctx: Context) => ctx.page === "issues";

export const defaultActions: Action[] = [
  { id: "go.next", label: "Next pull request", scope: "view-pulls",
    binding: { key: "j" }, priority: 0, when: onPullsListNotBoard,
    handler: () => stores().pulls.selectNextPR() },
  { id: "go.prev", label: "Previous pull request", scope: "view-pulls",
    binding: { key: "k" }, priority: 0, when: onPullsListNotBoard,
    handler: () => stores().pulls.selectPrevPR() },
  { id: "go.next.issues", label: "Next issue", scope: "view-issues",
    binding: { key: "j" }, priority: 0, when: onIssuesList,
    handler: () => stores().issues.selectNextIssue() },
  { id: "go.prev.issues", label: "Previous issue", scope: "view-issues",
    binding: { key: "k" }, priority: 0, when: onIssuesList,
    handler: () => stores().issues.selectPrevIssue() },
  // tab.toggle, escape.list, nav.pulls.list, nav.pulls.board, sidebar.toggle,
  // palette.open, cheatsheet.open, sync.repos, theme.toggle, nav.* — implement
  // each by mirroring the matching matrix row above.
];
```

Each row in the matrix becomes one entry in the array. For palette/cheatsheet handlers, import `openPalette`/`openCheatsheet` from the state stores (Task 17 / Task 24 introduce them; the catalog file's compile path is unblocked because those modules exist as empty shells from stage 2 onwards and grow in later stages).

- [ ] **Step 4: Run test to verify pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/actions.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/actions.ts frontend/src/lib/stores/keyboard/actions.test.ts
git commit -m "feat(keyboard): default action catalog matching pre-migration handleKeydown"
```

---

## Task 10: Migrate App.svelte handleKeydown

**Files:**
- Modify: `frontend/src/App.svelte`
- Test: `frontend/tests/e2e/keyboard-shortcuts-migration.spec.ts`

- [ ] **Step 1: Write the failing e2e test**

```ts
import { expect, test } from "@playwright/test";
import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => { await mockApi(page); });

test.describe("migrated global shortcuts", () => {
  test("j and k navigate the PR list", async ({ page }) => {
    await page.goto("/pulls");
    await page.waitForSelector("[data-test='pr-list']");
    await page.keyboard.press("j");
    await expect(page.locator(".pr-list-row.selected").first()).toBeVisible();
    await page.keyboard.press("k");
    await expect(page.locator(".pr-list-row.selected").first()).toBeVisible();
  });

  test("Cmd+[ toggles the sidebar", async ({ page }) => {
    await page.goto("/pulls");
    const sidebar = page.locator("[data-test='sidebar']");
    const wasCollapsed = (await sidebar.getAttribute("data-collapsed")) === "true";
    await page.keyboard.press("Meta+BracketLeft");
    await expect(sidebar).toHaveAttribute(
      "data-collapsed",
      (!wasCollapsed).toString(),
    );
  });
});
```

- [ ] **Step 2: Verify failure (still wired to old handleKeydown — test should pass against current main but fail mid-migration)**

Run: `cd frontend && bun run test:e2e keyboard-shortcuts-migration`
Expected: passes against current main; will be re-verified after migration to confirm parity.

- [ ] **Step 3: Replace App.svelte handleKeydown with registry installation**

In `frontend/src/App.svelte`, remove the `function handleKeydown(...) { ... }` block (lines around 304-415) and the `$effect` that adds the window listener. Add:

```svelte
<script lang="ts">
  // … existing imports
  import { registerScopedActions } from "./lib/stores/keyboard/registry.svelte.js";
  import { defaultActions, setStoreInstances } from "./lib/stores/keyboard/actions.js";
  import { dispatchKeydown } from "./lib/stores/keyboard/dispatch.svelte.js";
  import { buildContext } from "./lib/stores/keyboard/context.svelte.js";

  $effect(() => {
    if (!stores) return;
    setStoreInstances(() => stores!);
    const cleanup = registerScopedActions("app:defaults", defaultActions);
    const onKeydown = (e: KeyboardEvent) => dispatchKeydown(e, () => buildContext(stores!));
    window.addEventListener("keydown", onKeydown);
    return () => {
      window.removeEventListener("keydown", onKeydown);
      cleanup();
    };
  });
</script>
```

- [ ] **Step 4: Run e2e to confirm parity**

Run: `cd frontend && bun run test:e2e keyboard-shortcuts-migration`
Expected: PASS — all migrated shortcuts behave identically.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/App.svelte frontend/tests/e2e/keyboard-shortcuts-migration.spec.ts
git commit -m "feat(keyboard): migrate App.svelte handleKeydown to registry+dispatcher"
```

---

## Task 11: Wire MergeModal to push/pop modal frame

**Files:**
- Modify: `packages/ui/src/components/detail/MergeModal.svelte`
- Test: `packages/ui/src/components/detail/MergeModal.svelte.test.ts`

(All imports below stay inside `@middleman/ui` per the layering rule. The modal-stack store lives at `packages/ui/src/stores/keyboard/modal-stack.svelte.ts`.)

- [ ] **Step 1: Write the failing component test**

```ts
import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";

import MergeModal from "./MergeModal.svelte";
import { getStackDepth, resetModalStack } from "../../stores/keyboard/modal-stack.svelte.js";

describe("MergeModal modal frame integration", () => {
  afterEach(() => {
    cleanup();
    resetModalStack();
  });

  it("pushes a frame on mount and pops on unmount", () => {
    expect(getStackDepth()).toBe(0);
    const { unmount } = render(MergeModal, { props: { /* … minimal props */ } });
    expect(getStackDepth()).toBe(1);
    unmount();
    expect(getStackDepth()).toBe(0);
  });
});
```

- [ ] **Step 2: Verify failure**

Run: `cd packages/ui && bun run test MergeModal.svelte.test.ts`
Expected: FAIL (frame not pushed).

- [ ] **Step 3: Add push/pop in MergeModal.svelte**

In `MergeModal.svelte`'s `<script>`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";

  onMount(() => {
    return pushModalFrame("merge-modal", []);
  });
</script>
```

(The empty `actions` array means MergeModal's own keys — the Escape-to-close it already handles inline — stay as-is; the frame's role here is purely to block background dispatch.)

- [ ] **Step 4: Run test to verify pass**

Run: `cd packages/ui && bun run test MergeModal.svelte.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/components/detail/MergeModal.svelte packages/ui/src/components/detail/MergeModal.svelte.test.ts
git commit -m "feat(keyboard): MergeModal pushes a modal frame for shortcut isolation"
```

---

## Task 12: Wire the remaining existing modals

Apply the same pattern as Task 11 to:

- `packages/ui/src/components/detail/IssueDetail.svelte` — frame id `issue-detail-confirm`, only push when the embedded confirm sub-modal is open.
- `packages/ui/src/components/roborev/ShortcutHelpModal.svelte` — frame id `roborev-shortcut-help`.
- `frontend/src/lib/components/repositories/RepoIssueModal.svelte` — frame id `repo-issue-modal`.
- `frontend/src/lib/components/settings/RepoImportModal.svelte` — frame id `repo-import-modal`.

Each gets a small component test in the same style as Task 11's. Commit each modal as its own commit:

- [ ] `git commit -m "feat(keyboard): IssueDetail confirm sub-modal pushes a frame"`
- [ ] `git commit -m "feat(keyboard): ShortcutHelpModal pushes a frame"`
- [ ] `git commit -m "feat(keyboard): RepoIssueModal pushes a frame"`
- [ ] `git commit -m "feat(keyboard): RepoImportModal pushes a frame"`

---

## Task 13: Modal isolation e2e

**Files:**
- Test: `frontend/tests/e2e/keyboard-modal-isolation.spec.ts`

- [ ] **Step 1: Write the table-driven Playwright test**

```ts
import { expect, test } from "@playwright/test";
import { mockApi } from "./support/mockApi";

const MODAL_OPENERS: Array<{ name: string; open: (page: import("@playwright/test").Page) => Promise<void> }> = [
  { name: "merge", open: async (page) => { await page.goto("/pulls"); /* navigate, click merge button */ } },
  { name: "repo-import", open: async (page) => { await page.goto("/settings"); /* click "Add repo" */ } },
  // … one row per modal in the inventory
];

test.beforeEach(async ({ page }) => { await mockApi(page); });

for (const m of MODAL_OPENERS) {
  test(`${m.name} modal blocks background j/k`, async ({ page }) => {
    await m.open(page);
    const before = await page.locator(".pr-list-row.selected").count();
    await page.keyboard.press("j");
    const after = await page.locator(".pr-list-row.selected").count();
    expect(after).toBe(before); // selection did not change
  });
}
```

- [ ] **Step 2: Run test to verify pass**

Run: `cd frontend && bun run test:e2e keyboard-modal-isolation`
Expected: PASS for each modal.

- [ ] **Step 3: Commit**

```bash
git add frontend/tests/e2e/keyboard-modal-isolation.spec.ts
git commit -m "test(keyboard): modal frame blocks background shortcuts (table-driven)"
```

---

## Task 14: registerCheatsheetEntries on RepoTypeahead

**Files:**
- Modify: `frontend/src/lib/components/RepoTypeahead.svelte`
- Test: `frontend/src/lib/components/RepoTypeahead.svelte.test.ts`

- [ ] **Step 1: Write failing test**

```ts
import { cleanup, render } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";

import RepoTypeahead from "./RepoTypeahead.svelte";
import {
  getAllCheatsheetEntries,
  resetRegistry,
} from "../stores/keyboard/registry.svelte.js";

describe("RepoTypeahead cheatsheet entries", () => {
  afterEach(() => {
    cleanup();
    resetRegistry();
  });

  it("registers arrow-key navigation entries on mount", () => {
    render(RepoTypeahead, { props: { selected: undefined, onchange: () => {} } });
    const ids = getAllCheatsheetEntries().map((e) => e.id);
    expect(ids).toEqual(expect.arrayContaining(["repo-typeahead.next", "repo-typeahead.prev"]));
  });
});
```

- [ ] **Step 2: Verify failure**

Run: `cd frontend && bun run test src/lib/components/RepoTypeahead.svelte.test.ts`
Expected: FAIL.

- [ ] **Step 3: Add registerCheatsheetEntries call**

In `RepoTypeahead.svelte`'s `<script>`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { registerCheatsheetEntries } from "../stores/keyboard/registry.svelte.js";

  onMount(() => registerCheatsheetEntries("repo-typeahead", [
    { id: "repo-typeahead.next", label: "Next repo", binding: { key: "ArrowDown" }, scope: "view-pulls" },
    { id: "repo-typeahead.prev", label: "Previous repo", binding: { key: "ArrowUp" }, scope: "view-pulls" },
  ]));
</script>
```

- [ ] **Step 4: Verify pass**

Run: `cd frontend && bun run test src/lib/components/RepoTypeahead.svelte.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/RepoTypeahead.svelte frontend/src/lib/components/RepoTypeahead.svelte.test.ts
git commit -m "feat(keyboard): RepoTypeahead registers its arrow-nav as cheatsheet entries"
```

---

## Task 15: KbdBadge component

**Files:**
- Create: `packages/ui/src/components/keyboard/KbdBadge.svelte`
- Create: `packages/ui/src/components/keyboard/useKbdLabel.ts`
- Test: `packages/ui/src/components/keyboard/KbdBadge.test.ts`

- [ ] **Step 1: Write failing test**

```ts
import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vitest";

import KbdBadge from "./KbdBadge.svelte";

describe("KbdBadge", () => {
  afterEach(() => cleanup());

  it("renders Cmd glyph on macOS", () => {
    vi.stubGlobal("navigator", { platform: "MacIntel", userAgent: "Mac" });
    render(KbdBadge, { props: { binding: { key: "k", ctrlOrMeta: true } } });
    expect(screen.getByText(/⌘.*K/i)).toBeInTheDocument();
  });

  it("renders Ctrl glyph on Linux", () => {
    vi.stubGlobal("navigator", { platform: "Linux x86_64", userAgent: "X11" });
    render(KbdBadge, { props: { binding: { key: "k", ctrlOrMeta: true } } });
    expect(screen.getByText(/Ctrl.*K/i)).toBeInTheDocument();
  });

  it("includes a screen-reader-only expanded label", () => {
    render(KbdBadge, { props: { binding: { key: "k", ctrlOrMeta: true } } });
    expect(screen.getByText(/(Command|Control)-K/i)).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Verify failure**

Run: `cd packages/ui && bun run test KbdBadge.test.ts`
Expected: FAIL.

- [ ] **Step 3: Implement useKbdLabel + KbdBadge**

`useKbdLabel.ts`:

```ts
import type { KeySpec } from "../../stores/keyboard/keyspec.js";

const isMac =
  typeof navigator !== "undefined" &&
  /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent || "");

export function kbdGlyph(spec: KeySpec): string {
  const parts: string[] = [];
  if (spec.ctrlOrMeta) parts.push(isMac ? "⌘" : "Ctrl");
  if (spec.shift) parts.push(isMac ? "⇧" : "Shift");
  if (spec.alt) parts.push(isMac ? "⌥" : "Alt");
  parts.push(spec.key.length === 1 ? spec.key.toUpperCase() : spec.key);
  return parts.join(isMac ? "" : "+");
}

export function kbdAriaLabel(spec: KeySpec): string {
  const parts: string[] = [];
  if (spec.ctrlOrMeta) parts.push(isMac ? "Command" : "Control");
  if (spec.shift) parts.push("Shift");
  if (spec.alt) parts.push(isMac ? "Option" : "Alt");
  parts.push(spec.key);
  return parts.join("-");
}
```

`KbdBadge.svelte`:

```svelte
<script lang="ts">
  import type { KeySpec } from "../../../../frontend/src/lib/stores/keyboard/types.js";
  import { kbdGlyph, kbdAriaLabel } from "./useKbdLabel.js";

  interface Props { binding: KeySpec; }
  let { binding }: Props = $props();

  const glyph = $derived(kbdGlyph(binding));
  const aria = $derived(kbdAriaLabel(binding));
</script>

<kbd class="kbd-badge" aria-label={aria}>
  {glyph}
  <span class="sr-only">{aria}</span>
</kbd>

<style>
  .kbd-badge {
    display: inline-flex;
    align-items: center;
    padding: 1px 5px;
    border: 1px solid var(--border-default);
    border-radius: 3px;
    font-size: 11px;
    color: var(--text-secondary);
    background: var(--bg-inset);
    font-family: ui-monospace, monospace;
  }
  .sr-only {
    position: absolute;
    width: 1px; height: 1px;
    overflow: hidden;
    clip: rect(0,0,0,0);
  }
  @media (pointer: coarse) {
    .kbd-badge { display: none; }
  }
</style>
```

Export `KbdBadge` from `packages/ui/src/index.ts`.

- [ ] **Step 4: Verify pass**

Run: `cd packages/ui && bun run test KbdBadge.test.ts`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/components/keyboard/KbdBadge.svelte packages/ui/src/components/keyboard/useKbdLabel.ts packages/ui/src/components/keyboard/KbdBadge.test.ts packages/ui/src/index.ts
git commit -m "feat(keyboard): add KbdBadge primitive with platform-aware glyph"
```

---

## Task 16: Use KbdBadge in AppHeader

**Files:**
- Modify: `frontend/src/lib/components/layout/AppHeader.svelte`
- Test: `frontend/tests/e2e/keyboard-inline-hints.spec.ts`

- [ ] **Step 1: Write failing e2e**

```ts
import { expect, test } from "@playwright/test";
import { mockApi } from "./support/mockApi";

test.beforeEach(async ({ page }) => { await mockApi(page); });

test("Sync button shows kbd badge on pointer device", async ({ page }) => {
  await page.goto("/pulls");
  await expect(page.locator(".action-btn .kbd-badge")).toBeVisible();
});
```

- [ ] **Step 2: Verify failure**

Run: `cd frontend && bun run test:e2e keyboard-inline-hints`
Expected: FAIL.

- [ ] **Step 3: Render KbdBadge in AppHeader**

In `AppHeader.svelte`, alongside the existing Sync button (around the line `{syncing ? "Syncing..." : "Sync"}`):

```svelte
<script lang="ts">
  import { KbdBadge } from "@middleman/ui";
  // … existing imports
</script>

<button class="action-btn" onclick={handleSync} disabled={syncing}>
  {syncing ? "Syncing..." : "Sync"}
  <KbdBadge binding={{ key: "s" /* whatever the actions.ts binding is */ }} />
</button>
```

(Apply the same pattern to the theme-toggle and sidebar-toggle buttons in their respective components, using each action's binding.)

- [ ] **Step 4: Verify pass**

Run: `cd frontend && bun run test:e2e keyboard-inline-hints`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/layout/AppHeader.svelte frontend/tests/e2e/keyboard-inline-hints.spec.ts
git commit -m "feat(keyboard): render KbdBadge next to chrome buttons"
```

---

## Task 17: Palette shell and open/close

**Files:**
- Create: `frontend/src/lib/components/keyboard/Palette.svelte`
- Create: `frontend/src/lib/stores/keyboard/palette-state.svelte.ts`
- Test: `frontend/src/lib/components/keyboard/Palette.svelte.test.ts`
- Test: `frontend/tests/e2e/palette-open-close.spec.ts`

- [ ] **Step 1: Write the failing component test**

```ts
import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vitest";

import Palette from "./Palette.svelte";
import { openPalette, closePalette, isPaletteOpen, resetPaletteState } from "../../stores/keyboard/palette-state.svelte.js";

describe("Palette", () => {
  afterEach(() => { cleanup(); resetPaletteState(); });

  it("renders only when isPaletteOpen is true", () => {
    const { rerender } = render(Palette, { props: {} });
    expect(screen.queryByRole("dialog")).toBeNull();
    openPalette();
    rerender({});
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Implement palette-state and Palette.svelte shell**

`palette-state.svelte.ts`:

```ts
let open = $state(false);
let lastFocusedElement: HTMLElement | null = null;

export function openPalette(): void {
  if (typeof document !== "undefined") {
    lastFocusedElement = document.activeElement as HTMLElement | null;
  }
  open = true;
}

export function closePalette(): void {
  open = false;
  if (lastFocusedElement && typeof lastFocusedElement.focus === "function") {
    lastFocusedElement.focus();
    lastFocusedElement = null;
  }
}

export function togglePalette(): void {
  open ? closePalette() : openPalette();
}

export function isPaletteOpen(): boolean { return open; }
export function resetPaletteState(): void { open = false; lastFocusedElement = null; }
```

`Palette.svelte` (shell):

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { isPaletteOpen, closePalette } from "../../stores/keyboard/palette-state.svelte.js";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";
  import type { Action } from "../../stores/keyboard/types.js";

  let dialogEl: HTMLDivElement | undefined = $state();

  $effect(() => {
    if (!isPaletteOpen()) return;
    const closeAction: Action = {
      id: "palette.close", label: "Close palette", scope: "global",
      binding: [{ key: "Escape" }, { key: "k", ctrlOrMeta: true }, { key: "p", ctrlOrMeta: true }],
      priority: 100, when: () => true, handler: () => closePalette(),
    };
    return pushModalFrame("palette", [closeAction]);
  });
</script>

{#if isPaletteOpen()}
  <div class="palette-backdrop" onclick={closePalette}></div>
  <div bind:this={dialogEl} class="palette" role="dialog" aria-modal="true" aria-label="Command palette">
    <input class="palette-input" placeholder="Search loaded PRs, issues, commands…" />
    <div class="palette-body">
      <div class="palette-list"></div>
      <div class="palette-preview"></div>
    </div>
    <div class="palette-footer">↑↓ navigate · ⏎ run · esc close</div>
  </div>
{/if}

<style>
  .palette-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.55); z-index: 100; }
  .palette {
    position: fixed; top: 80px; left: 50%; transform: translateX(-50%);
    width: 920px; max-width: calc(100vw - 32px); height: 480px;
    display: grid; grid-template-rows: auto 1fr auto;
    background: var(--bg-surface); border: 1px solid var(--border-default);
    border-radius: 10px; box-shadow: var(--shadow-lg); z-index: 101;
  }
  .palette-input { padding: 12px 16px; border: none; border-bottom: 1px solid var(--border-muted); background: transparent; }
  .palette-body { display: grid; grid-template-columns: 360px 1fr; }
  .palette-list { border-right: 1px solid var(--border-muted); overflow-y: auto; }
  .palette-preview { padding: 16px; }
  .palette-footer { padding: 6px 12px; border-top: 1px solid var(--border-muted); font-size: 11px; color: var(--text-secondary); }
</style>
```

Render `<Palette />` from `App.svelte` near the root.

- [ ] **Step 3: Write the e2e**

```ts
test("Cmd+K opens and closes the palette; focus is restored", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  await expect(page.locator("[role='dialog'][aria-label='Command palette']")).toBeVisible();
  await page.keyboard.press("Meta+K");
  await expect(page.locator("[role='dialog'][aria-label='Command palette']")).not.toBeVisible();
});
```

- [ ] **Step 4: Run tests**

Run: `cd frontend && bun run test src/lib/components/keyboard/Palette.svelte.test.ts && bun run test:e2e palette-open-close`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/components/keyboard/Palette.svelte frontend/src/lib/stores/keyboard/palette-state.svelte.ts frontend/src/lib/components/keyboard/Palette.svelte.test.ts frontend/tests/e2e/palette-open-close.spec.ts frontend/src/App.svelte
git commit -m "feat(keyboard): palette shell with open/close toggle and focus restore"
```

---

## Task 18: Palette focus trap

**Files:**
- Modify: `frontend/src/lib/components/keyboard/Palette.svelte`
- Test: `frontend/tests/e2e/palette-focus-trap.spec.ts`

- [ ] **Step 1: Write failing e2e**

```ts
test("tab cycles within the open palette", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  const input = page.locator(".palette-input");
  await expect(input).toBeFocused();
  await page.keyboard.press("Tab");
  // …focus should move within palette and never escape to background
});
```

- [ ] **Step 2: Add focus-trap effect to Palette.svelte**

```svelte
$effect(() => {
  if (!isPaletteOpen() || !dialogEl) return;
  const focusable = () => Array.from(dialogEl!.querySelectorAll<HTMLElement>(
    "input, button, [tabindex]:not([tabindex='-1'])"
  )).filter((e) => !e.hasAttribute("disabled"));
  focusable()[0]?.focus();
  function trap(e: KeyboardEvent) {
    if (e.key !== "Tab") return;
    const els = focusable();
    if (els.length === 0) return;
    const first = els[0]!;
    const last = els[els.length - 1]!;
    if (e.shiftKey && document.activeElement === first) { last.focus(); e.preventDefault(); }
    else if (!e.shiftKey && document.activeElement === last) { first.focus(); e.preventDefault(); }
  }
  dialogEl.addEventListener("keydown", trap);
  return () => dialogEl?.removeEventListener("keydown", trap);
});
```

- [ ] **Step 3: Verify pass**

Run: `cd frontend && bun run test:e2e palette-focus-trap`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/keyboard/Palette.svelte frontend/tests/e2e/palette-focus-trap.spec.ts
git commit -m "feat(keyboard): trap focus inside the open palette"
```

---

## Task 19: Palette search and result groups

**Files:**
- Modify: `frontend/src/lib/components/keyboard/Palette.svelte`
- Create: `frontend/src/lib/stores/keyboard/palette-search.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/palette-search.svelte.test.ts`

- [ ] **Step 1: Write failing tests for prefix parsing and grouping**

```ts
import { describe, expect, it } from "vitest";

import { parsePaletteQuery, groupResults } from "./palette-search.svelte.js";

describe("parsePaletteQuery", () => {
  it("recognizes >, pr:, issue: prefixes", () => {
    expect(parsePaletteQuery(">refresh")).toEqual({ scope: "command", query: "refresh" });
    expect(parsePaletteQuery("pr:fix")).toEqual({ scope: "pr", query: "fix" });
    expect(parsePaletteQuery("issue:bug")).toEqual({ scope: "issue", query: "bug" });
    expect(parsePaletteQuery("plain")).toEqual({ scope: "all", query: "plain" });
  });

  it("returns reserved-prefix marker for repo: and ws:", () => {
    expect(parsePaletteQuery("repo:abc")).toEqual({ scope: "reserved", query: "" });
    expect(parsePaletteQuery("ws:abc")).toEqual({ scope: "reserved", query: "" });
  });
});
```

- [ ] **Step 2: Implement palette-search**

```ts
export type ParsedQuery =
  | { scope: "command"; query: string }
  | { scope: "pr"; query: string }
  | { scope: "issue"; query: string }
  | { scope: "all"; query: string }
  | { scope: "reserved"; query: "" };

export function parsePaletteQuery(input: string): ParsedQuery {
  if (input.startsWith(">")) return { scope: "command", query: input.slice(1).trim() };
  if (input.startsWith("pr:")) return { scope: "pr", query: input.slice(3).trim() };
  if (input.startsWith("issue:")) return { scope: "issue", query: input.slice(6).trim() };
  if (input.startsWith("repo:") || input.startsWith("ws:")) return { scope: "reserved", query: "" };
  return { scope: "all", query: input.trim() };
}

// groupResults({ commands, pulls, issues, parsed }) — fuzzy-match each list
// against parsed.query, cap each group at 10, return { commands, pulls, issues }.
```

- [ ] **Step 3: Wire into Palette.svelte**

Add result rendering grouped by Commands / Pull requests / Issues. For `scope: "reserved"`, render the placeholder row described in the spec.

- [ ] **Step 4: Run tests**

Run: `cd frontend && bun run test src/lib/stores/keyboard/palette-search.svelte.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/stores/keyboard/palette-search.svelte.ts frontend/src/lib/stores/keyboard/palette-search.svelte.test.ts frontend/src/lib/components/keyboard/Palette.svelte
git commit -m "feat(keyboard): palette search with prefix parsing and grouped results"
```

---

## Task 20: Palette preview pane

**Files:**
- Modify: `frontend/src/lib/components/keyboard/Palette.svelte`
- Test: `frontend/src/lib/components/keyboard/Palette.svelte.test.ts`

- [ ] **Step 1: Write failing component test**

```ts
it("renders preview for the highlighted PR", async () => {
  // mount palette with a fake PR result; assert preview shows title/repo/state
});
```

- [ ] **Step 2: Add preview rendering**

The preview pane reads from the highlighted result. For PRs: title, repo (`owner/name`), state badge, last activity, and a body excerpt (first ~200 chars of `Body`). For issues: same shape. For commands: action label, scope description, "Available when …" line.

- [ ] **Step 3: Verify pass**

Run: `cd frontend && bun run test src/lib/components/keyboard/Palette.svelte.test.ts`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/keyboard/Palette.svelte frontend/src/lib/components/keyboard/Palette.svelte.test.ts
git commit -m "feat(keyboard): palette preview pane reflects highlighted result"
```

---

## Task 21: Palette command e2e (`>` prefix + safe global)

**Files:**
- Test: `frontend/tests/e2e/palette-commands.spec.ts`

- [ ] **Step 1: Write the e2e**

```ts
test("> filters to commands; running Open settings navigates", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  await page.locator(".palette-input").fill(">settings");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/settings/);
});

test("typing a single character in the search input does not fire global shortcuts", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  const before = await page.locator(".pr-list-row.selected").count();
  await page.locator(".palette-input").fill("j");
  const after = await page.locator(".pr-list-row.selected").count();
  expect(after).toBe(before);
});

test("Cmd+P inside palette closes the palette instead of opening browser print", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  await page.keyboard.press("Meta+P");
  await expect(page.locator("[role='dialog']")).not.toBeVisible();
});
```

- [ ] **Step 2: Run tests**

Run: `cd frontend && bun run test:e2e palette-commands`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add frontend/tests/e2e/palette-commands.spec.ts
git commit -m "test(keyboard): palette command filtering and dispatch isolation e2e"
```

---

## Task 22: Recents persistence

**Files:**
- Create: `frontend/src/lib/stores/keyboard/recents.svelte.ts`
- Test: `frontend/src/lib/stores/keyboard/recents.svelte.test.ts`

- [ ] **Step 1: Write failing tests for read/write/dedupe/malformed/stale**

```ts
import { beforeEach, describe, expect, it } from "vitest";

import { readRecents, writeRecent, pruneRecents } from "./recents.svelte.js";
import type { RecentsState } from "./types.js";

beforeEach(() => localStorage.clear());

describe("recents", () => {
  it("returns empty for missing key", () => {
    expect(readRecents()).toEqual({ version: 1, items: [] });
  });

  it("malformed JSON is ignored and overwritten", () => {
    localStorage.setItem("middleman-palette-recents", "not-json");
    expect(readRecents()).toEqual({ version: 1, items: [] });
    expect(localStorage.getItem("middleman-palette-recents")).toBe(JSON.stringify({ version: 1, items: [] }));
  });

  it("version mismatch is treated as empty", () => {
    localStorage.setItem("middleman-palette-recents", JSON.stringify({ version: 0, items: [] }));
    expect(readRecents().items).toHaveLength(0);
  });

  it("dedupe by kind+ref, max 8", () => {
    for (let i = 0; i < 10; i++) {
      writeRecent("pr", { itemType: "pr", provider: "github", owner: "a", name: "b", repoPath: "a/b", number: i });
    }
    expect(readRecents().items).toHaveLength(8);
  });

  it("drops items with kinds outside pr|issue", () => {
    localStorage.setItem("middleman-palette-recents", JSON.stringify({ version: 1, items: [
      { kind: "pr", ref: { itemType: "pr", provider: "g", owner: "a", name: "b", repoPath: "a/b", number: 1 }, lastSelectedAt: "2026-01-01T00:00:00Z" },
      { kind: "future-kind", ref: {}, lastSelectedAt: "2026-01-01T00:00:00Z" },
    ]}));
    expect(readRecents().items).toHaveLength(1);
  });
});
```

- [ ] **Step 2: Implement recents store**

```ts
import type { RecentsState } from "./types.js";
import type { RoutedItemRef } from "@middleman/ui/routes";

const KEY = "middleman-palette-recents";
const MAX_ITEMS = 8;

export function readRecents(): RecentsState {
  const raw = localStorage.getItem(KEY);
  if (!raw) return { version: 1, items: [] };
  try {
    const parsed = JSON.parse(raw);
    if (!parsed || parsed.version !== 1 || !Array.isArray(parsed.items)) {
      const empty = { version: 1, items: [] } as RecentsState;
      localStorage.setItem(KEY, JSON.stringify(empty));
      return empty;
    }
    parsed.items = parsed.items.filter(
      (i: { kind: unknown }) => i.kind === "pr" || i.kind === "issue",
    );
    return parsed as RecentsState;
  } catch {
    const empty = { version: 1, items: [] } as RecentsState;
    localStorage.setItem(KEY, JSON.stringify(empty));
    return empty;
  }
}

export function writeRecent(kind: "pr" | "issue", ref: RoutedItemRef): void {
  const state = readRecents();
  const dedupeKey = `${kind}|${JSON.stringify(ref)}`;
  state.items = state.items.filter((i) => `${i.kind}|${JSON.stringify(i.ref)}` !== dedupeKey);
  state.items.unshift({ kind, ref, lastSelectedAt: new Date().toISOString() });
  if (state.items.length > MAX_ITEMS) state.items.length = MAX_ITEMS;
  localStorage.setItem(KEY, JSON.stringify(state));
}

export function pruneRecents(filter: (item: RecentsState["items"][number]) => boolean): void {
  const state = readRecents();
  state.items = state.items.filter(filter);
  localStorage.setItem(KEY, JSON.stringify(state));
}
```

- [ ] **Step 3: Verify pass**

Run: `cd frontend && bun run test src/lib/stores/keyboard/recents.svelte.test.ts`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/stores/keyboard/recents.svelte.ts frontend/src/lib/stores/keyboard/recents.svelte.test.ts
git commit -m "feat(keyboard): recents store with malformed-JSON handling and stale pruning"
```

---

## Task 23: Empty-state palette renders recents + commands

**Files:**
- Modify: `frontend/src/lib/components/keyboard/Palette.svelte`
- Test: `frontend/tests/e2e/palette-recents.spec.ts`

- [ ] **Step 1: Write failing e2e**

```ts
test("recents persist round-trip: select PR, close, reopen, see at top", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("Meta+K");
  await page.locator(".palette-list .palette-row").first().click();
  await page.keyboard.press("Meta+K");
  await expect(page.locator(".palette-list .palette-row").first()).toContainText(/recently/i);
});
```

- [ ] **Step 2: Implement empty-state rendering in Palette.svelte**

When the search input is empty, render a "Recent" section with `readRecents()` results, then "Commands" with currently-applicable commands. On selecting a content result, call `writeRecent(kind, ref)`.

- [ ] **Step 3: Verify pass**

Run: `cd frontend && bun run test:e2e palette-recents`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/keyboard/Palette.svelte frontend/tests/e2e/palette-recents.spec.ts
git commit -m "feat(keyboard): empty-state palette shows recents + applicable commands"
```

---

## Task 24: Cheatsheet modal

**Files:**
- Create: `frontend/src/lib/components/keyboard/Cheatsheet.svelte`
- Create: `frontend/src/lib/stores/keyboard/cheatsheet-state.svelte.ts`
- Test: `frontend/src/lib/components/keyboard/Cheatsheet.svelte.test.ts`
- Test: `frontend/tests/e2e/cheatsheet.spec.ts`

- [ ] **Step 1: Write failing tests**

Component test asserts category grouping ("On this view", "Global", "Commands") and filter input narrowing. E2E asserts `?` opens it and Escape closes it.

```ts
test("? opens the cheatsheet and shows j/k under On this view", async ({ page }) => {
  await page.goto("/pulls");
  await page.keyboard.press("?");
  const sheet = page.locator("[role='dialog'][aria-label='Keyboard shortcuts']");
  await expect(sheet).toBeVisible();
  await expect(sheet.getByText(/j/i)).toBeVisible();
});
```

- [ ] **Step 2: Implement Cheatsheet shell**

`cheatsheet-state.svelte.ts` mirrors `palette-state.svelte.ts` (open/close + focus restore).

`Cheatsheet.svelte`:

```svelte
<script lang="ts">
  import { isCheatsheetOpen, closeCheatsheet } from "../../stores/keyboard/cheatsheet-state.svelte.js";
  import { pushModalFrame } from "../../stores/keyboard/modal-stack.svelte.js";
  import {
    getAllActions, getAllCheatsheetEntries,
  } from "../../stores/keyboard/registry.svelte.js";
  import { buildContext } from "../../stores/keyboard/context.svelte.js";
  // … category derivation, filter input
</script>

{#if isCheatsheetOpen()}
  <div class="palette-backdrop" onclick={closeCheatsheet}></div>
  <div role="dialog" aria-modal="true" aria-label="Keyboard shortcuts" class="cheatsheet">
    <input class="cheatsheet-filter" placeholder="Filter shortcuts…" />
    <section><h3>On this view</h3>{/* grouped rows */}</section>
    <section><h3>Global</h3>{/* … */}</section>
    <section><h3>Commands</h3>{/* … */}</section>
  </div>
{/if}
```

Render `<Cheatsheet />` from App.svelte. Add the `cheatsheet.open` action's handler to call `openCheatsheet()`.

- [ ] **Step 3: Verify pass**

Run: `cd frontend && bun run test src/lib/components/keyboard/Cheatsheet.svelte.test.ts && bun run test:e2e cheatsheet`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/components/keyboard/Cheatsheet.svelte frontend/src/lib/stores/keyboard/cheatsheet-state.svelte.ts frontend/src/lib/components/keyboard/Cheatsheet.svelte.test.ts frontend/tests/e2e/cheatsheet.spec.ts frontend/src/App.svelte
git commit -m "feat(keyboard): cheatsheet modal with category grouping and filter"
```

---

## Task 25: Extract PR detail action closures

**Files:**
- Create: `packages/ui/src/components/detail/keyboard-actions.ts` (in `@middleman/ui` so the buttons can import it)
- Modify: `packages/ui/src/components/detail/ApproveButton.svelte`
- Modify: `packages/ui/src/components/detail/MergeModal.svelte`
- Modify: `packages/ui/src/components/detail/ReadyForReviewButton.svelte`
- Modify: `packages/ui/src/components/detail/ApproveWorkflowsButton.svelte`
- Test: `packages/ui/src/components/detail/keyboard-actions.test.ts`

**Pre-extraction inventory.** Before writing the closures, read each existing button to record:

- The button's render guard (the `{#if …}` condition or a `disabled` expression).
- The button's click handler (the body of `onclick={…}`).
- The button's input props (typically the selected PR, viewer permissions, and any callback props for refresh / error / state-update).
- The mutation endpoint each button calls (e.g. `POST /pulls/.../approve`, `POST /pulls/.../merge`).
- The post-mutation refresh path: which store getters are called (`stores.pulls.refreshPR(...)`, `stores.detail.invalidate(...)`, etc.) and which flash/toast paths are reused on success/failure.
- Modal-opening buttons: which component owns the modal-open `$state` and how the click handler flips it.

Record the inventory at the top of `keyboard-actions.ts` as a comment block so reviewers can compare against the existing buttons. Each closure pair (`canX`, `runX`) takes a single context argument that bundles the same inputs the button currently has — typically `{ pr, viewerCan, stores, onError, onAfter }`. Define an interface `PRDetailActionInput` in the same file that captures those inputs and is shared by every closure pair. The button then becomes a thin shell that constructs the input object from its existing props and calls `canX(input)` for the render guard and `runX(input)` for the click handler. The app shell's palette command registration constructs the same input object from the active `Context` plus the store instances.

- [ ] **Step 1: Write failing tests**

```ts
import { describe, expect, it } from "vitest";

import { canApprovePR, runApprovePR } from "./keyboard-actions.js";

describe("keyboard-actions", () => {
  it("canApprovePR returns false for closed PR", () => {
    expect(canApprovePR({ pr: { State: "closed" }, viewerCan: { approve: true } } as never)).toBe(false);
  });
  it("canApprovePR returns true for open PR with approve permission", () => {
    expect(canApprovePR({ pr: { State: "open" }, viewerCan: { approve: true } } as never)).toBe(true);
  });
});
```

- [ ] **Step 2: Implement closures**

```ts
// packages/ui/src/components/detail/keyboard-actions.ts
import type { PullRequest } from "../../api/types.js";

export interface PRDetailActionInput {
  pr: PullRequest;
  viewerCan: { approve: boolean; merge: boolean; markReady: boolean; approveWorkflows: boolean };
  stores: { pulls: { refreshPR(ref: PullRequest): Promise<void> } };
  client: typeof import("../../api/client.js")["client"];
  setMergeModalOpen?: (open: boolean) => void; // owned by the PR detail view
  onError?: (msg: string) => void;
}

export function canApprovePR(input: PRDetailActionInput): boolean {
  return input.pr.State === "open" && input.viewerCan.approve;
}

export async function runApprovePR(input: PRDetailActionInput): Promise<void> {
  const { error } = await input.client.POST(
    /* providerItemPath("pulls", ref, "/approve") — copy from existing ApproveButton */ as never,
    {} as never,
  );
  if (error) {
    input.onError?.(error.detail ?? error.title ?? "approve failed");
    throw new Error("approve failed");
  }
  await input.stores.pulls.refreshPR(input.pr);
}

// Repeat for canOpenMergeDialog/runOpenMergeDialog (the run flips setMergeModalOpen(true)),
// canMarkReady/runMarkReady, canApproveWorkflows/runApproveWorkflows. Each pair mirrors the
// existing button's logic exactly per the inventory at the top of this file.
```

- [ ] **Step 3: Refactor each existing button**

In `ApproveButton.svelte`, replace the render guard and click handler with:

```svelte
<script lang="ts">
  import { canApprovePR, runApprovePR } from "../../../../packages/ui/src/components/detail/keyboard-actions.js";
  // …
</script>

{#if canApprovePR(ctx)}
  <button onclick={() => runApprovePR(ctx)}>Approve</button>
{/if}
```

Repeat for the three other buttons.

- [ ] **Step 4: Verify pass**

Run: `cd packages/ui && bun run test components/detail/keyboard-actions.test.ts && bun run test components/detail/`
Expected: existing button tests still pass; new closure tests pass.

- [ ] **Step 5: Commit**

```bash
git add packages/ui/src/components/detail/keyboard-actions.ts packages/ui/src/components/detail/keyboard-actions.test.ts packages/ui/src/components/detail/ApproveButton.svelte packages/ui/src/components/detail/MergeModal.svelte packages/ui/src/components/detail/ReadyForReviewButton.svelte packages/ui/src/components/detail/ApproveWorkflowsButton.svelte
git commit -m "refactor(keyboard): extract shared canX/runX closures from PR detail buttons"
```

---

## Task 26: Register PR detail palette commands

**Files:**
- Modify: PR detail view component (the route's `pulls/detail` page mount)
- Test: `frontend/tests/e2e/palette-pr-detail-commands.spec.ts`

- [ ] **Step 1: Write failing e2e**

```ts
test("Approve PR runs from the palette and triggers the existing approve flow", async ({ page }) => {
  await page.goto("/pulls/detail?…"); // mock a PR detail
  await page.keyboard.press("Meta+K");
  await page.locator(".palette-input").fill("approve pr");
  await page.keyboard.press("Enter");
  // expect the approval API call (mockApi will assert)
});

test("Approve PR is absent from palette when PR is closed", async ({ page }) => {
  // mount a closed PR; open palette; type "approve"; assert no Approve PR row
});
```

- [ ] **Step 2: Register pr-detail-actions on PR detail mount**

In the PR detail view component, add:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { registerScopedActions } from "../stores/keyboard/registry.svelte.js";
  import {
    canApprovePR, runApprovePR,
    canOpenMerge, runOpenMerge,
    canMarkReady, runMarkReady,
    canApproveWorkflows, runApproveWorkflows,
  } from "@middleman/ui/components/detail/keyboard-actions";

  onMount(() => registerScopedActions("pr-detail-actions", [
    { id: "pr.approve", label: "Approve PR", scope: "detail-pr", binding: null, priority: 0,
      when: canApprovePR, handler: runApprovePR },
    { id: "pr.merge", label: "Open merge dialog", scope: "detail-pr", binding: null, priority: 0,
      when: canOpenMerge, handler: runOpenMerge },
    { id: "pr.ready", label: "Mark ready for review", scope: "detail-pr", binding: null, priority: 0,
      when: canMarkReady, handler: runMarkReady },
    { id: "pr.approveWorkflows", label: "Approve workflows", scope: "detail-pr", binding: null, priority: 0,
      when: canApproveWorkflows, handler: runApproveWorkflows },
  ]));
</script>
```

- [ ] **Step 3: Verify pass**

Run: `cd frontend && bun run test:e2e palette-pr-detail-commands`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add packages/ui/src/views/PullDetailView.svelte frontend/tests/e2e/palette-pr-detail-commands.spec.ts
git commit -m "feat(keyboard): register PR detail palette commands gated by existing button availability"
```

(The exact PR detail view file may differ; locate via `grep -rn "pulls/detail" packages/ui/src/views/`.)

---

## Task 27: Coverage harmonization (only if gaps surface)

**Files:**
- Test: `frontend/tests/e2e/keyboard-cross-stage.spec.ts`

- [ ] **Step 1: Add only the cross-stage tests that did not fit earlier**

If, while wiring stages 17–26, an integration case requires multiple stages assembled, write a Playwright test for it here. Examples that may surface:

- Opening the palette while a modal is already open and verifying Escape closes the palette but leaves the underlying modal in place.
- Verifying that `?` while the palette is open is treated as a literal character in the search input rather than opening the cheatsheet (because modal stack consumes it).

- [ ] **Step 2: Commit**

```bash
git add frontend/tests/e2e/keyboard-cross-stage.spec.ts
git commit -m "test(keyboard): cross-stage integration coverage"
```

If no gaps surfaced, omit this task entirely.

---

## Self-Review

**Spec coverage:** Each of the 11 stages in the spec maps to one or more tasks above:

- Stage 1 (registry + dispatch) → Tasks 1, 2, 3, 4, 5, 6, 7, 8.
- Stage 2 (migrate App.svelte) → Tasks 9, 10.
- Stage 3 (modal stack + existing modals) → Tasks 11, 12, 13.
- Stage 4 (cheatsheet entries from per-component handlers) → Task 14.
- Stage 5 (KbdBadge) → Tasks 15, 16.
- Stage 6 (palette shell + open/close) → Tasks 17, 18.
- Stage 7 (palette search and previews) → Tasks 19, 20, 21.
- Stage 8 (recents) → Tasks 22, 23.
- Stage 9 (cheatsheet modal) → Task 24.
- Stage 10 (PR detail commands) → Tasks 25, 26.
- Stage 11 (coverage harmonization) → Task 27.

**Placeholder scan:** No "TBD" / "TODO" / "implement later" entries. Step 9 of Task 9 references "transcribe from existing handleKeydown" — the handleKeydown text is already in the engineer's working tree; that is a concrete instruction, not a placeholder. Tasks 25 and 26 reference exact files in the existing button refactors; if button signatures differ from what is shown, the engineer reads the file and adapts.

**Type consistency:** The same `Action`, `KeySpec`, `Context`, `RecentsState`, `CheatsheetEntry`, and `ScopeTag` shapes are used across all tasks. Function names (`registerScopedActions`, `pushModalFrame`, `dispatchKeydown`, `buildContext`, `readRecents`, `writeRecent`, `openPalette`, `closePalette`, `openCheatsheet`, `closeCheatsheet`) are consistent across tasks.
