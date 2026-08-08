import { Effect } from "effect";
import { getStack } from "./modal-stack.svelte.js";
import { showFlash } from "../flash.svelte.js";
import { getAllActions } from "./registry.svelte.js";
import { isTerminalKeyboardTarget, shouldIgnoreGlobalShortcutTarget } from "../../utils/keyboardShortcuts.js";
import type { AppRuntime, AppServices } from "../../app/runtime.js";
import type { KeyboardHandlerResult } from "./keyspec.js";
import type { Action, Context, KeySpec } from "./types.js";

const RESERVED_WHILE_MODAL_OPEN: KeySpec[] = [
  { key: "k", ctrlOrMeta: true },
  { key: "p", ctrlOrMeta: true },
  { key: "p", ctrlOrMeta: true, shift: true },
];

const SCOPE_SPECIFICITY: Record<Action["scope"], number> = {
  "detail-pr": 30,
  "detail-issue": 30,
  detail: 30,
  "view-pulls": 20,
  "view-issues": 20,
  global: 10,
};

export function dispatchKeydown(event: KeyboardEvent, contextProvider: () => Context, runtime: AppRuntime): void {
  // A focused terminal owns every key except the explicit shifted palette
  // chord, and it otherwise outranks the modal stack too: a frame that did not
  // trap focus cannot be holding the keyboard the user is typing into. Not even
  // preventDefault: the keystroke is xterm's, and suppressing the default is how
  // it would fail to arrive. Popovers keep their own window Escape listeners.
  if (isTerminalKeyboardTarget(event.target) && !isTerminalPaletteShortcut(event)) return;

  const stack = getStack();
  if (stack.length > 0) {
    const modalCtx = contextProvider();
    for (let i = stack.length - 1; i >= 0; i--) {
      const frame = stack[i]!;
      for (const a of frame.actions) {
        if (!matches(a.binding, event)) continue;
        if (a.when && !a.when(modalCtx)) continue;
        event.preventDefault();
        runHandler(a, modalCtx, runtime);
        return;
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
    (a) => a.binding !== null && matches(a.binding, event) && a.when(ctx) && (!editable || hasModifier(a.binding)),
  );
  if (matchingActions.length === 0) return;

  matchingActions.sort((a, b) => {
    const sd = SCOPE_SPECIFICITY[b.scope] - SCOPE_SPECIFICITY[a.scope];
    if (sd !== 0) return sd;
    return b.priority - a.priority;
  });

  event.preventDefault();
  runHandler(matchingActions[0]!, ctx, runtime);
}

function isTerminalPaletteShortcut(event: KeyboardEvent): boolean {
  return event.key.toLowerCase() === "k" && (event.ctrlKey || event.metaKey) && event.shiftKey && !event.altKey;
}

interface RunnableAction {
  id: string;
  handler: (ctx: Context) => KeyboardHandlerResult;
}

const inFlight = new Set<string>();

function runHandler(action: RunnableAction, ctx: Context, runtime: AppRuntime): void {
  if (inFlight.has(action.id)) return;
  inFlight.add(action.id);
  runtime.runCommand(
    Effect.try({
      try: () => action.handler(ctx),
      catch: (cause) => cause,
    }).pipe(
      Effect.flatMap(
        (result): Effect.Effect<void, unknown, AppServices> => (result === undefined ? Effect.void : result),
      ),
      Effect.ensuring(Effect.sync(() => inFlight.delete(action.id))),
    ),
    {
      operation: "run keyboard action",
      safeContext: { actionId: action.id },
      onFailure: (error) => surfaceError(action.id, error),
    },
  );
}

function surfaceError(actionId: string, err: unknown): void {
  const msg = err instanceof Error && err.message ? err.message : "Command failed";
  if (!(err instanceof Error) || !err.message) {
    console.error(`keyboard action ${actionId} failed`, err);
  }
  showFlash(msg, { tone: "danger" });
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
