import { getStack } from "@middleman/ui/stores/keyboard/modal-stack";
import { showFlash } from "@middleman/ui/stores/flash";
import { getAllActions } from "./registry.svelte.js";
import { shouldIgnoreGlobalShortcutTarget } from "../../utils/keyboardShortcuts.js";
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

export function dispatchKeydown(event: KeyboardEvent, contextProvider: () => Context): void {
  const stack = getStack();
  if (stack.length > 0) {
    const modalCtx = contextProvider();
    for (let i = stack.length - 1; i >= 0; i--) {
      const frame = stack[i]!;
      for (const a of frame.actions) {
        if (!matches(a.binding, event)) continue;
        if (a.when && !a.when(modalCtx)) continue;
        event.preventDefault();
        runHandler(a, modalCtx);
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
  runHandler(matchingActions[0]!, ctx);
}

interface RunnableAction {
  id: string;
  handler: (ctx: Context) => void | Promise<void>;
}

const inFlight = new Set<string>();

export type CommandHandlerResult = void | Promise<void>;

export function handleCommandResult(
  result: CommandHandlerResult,
  onRejected: (err: unknown) => void,
  onSettled?: () => void,
): boolean {
  if (result === undefined) return false;
  result.catch(onRejected).finally(onSettled);
  return true;
}

function runHandler(action: RunnableAction, ctx: Context): void {
  if (inFlight.has(action.id)) return;
  try {
    const result = action.handler(ctx);
    if (
      handleCommandResult(
        result,
        (err) => surfaceError(action.id, err),
        () => inFlight.delete(action.id),
      )
    ) {
      inFlight.add(action.id);
    }
  } catch (err) {
    surfaceError(action.id, err);
  }
}

function surfaceError(actionId: string, err: unknown): void {
  const msg = err instanceof Error && err.message ? err.message : "Command failed";
  if (!(err instanceof Error) || !err.message) {
    console.error(`keyboard action ${actionId} failed`, err);
  }
  showFlash(msg);
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
