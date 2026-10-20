import { Effect } from "effect";
import { ApiProblemError } from "../../api/effect-errors.js";
import { worklistFailureMessage } from "../../api/worklists.js";
import type { AppExecution, AppRuntime } from "../../app/runtime.js";
import { showFlash } from "../../stores/flash.svelte.js";
import type { WorklistsStore } from "../../stores/worklists.svelte.js";

/**
 * Dismiss and Undo on the worklists page. Every transition enters a new state
 * object, and a completion acts only while the machine still holds the exact
 * state that started it, so a late response can never act on a newer one.
 * Successes navigate only while the page still shows the list it showed when
 * the request started. The store mutation always finishes; leaving the page
 * only cancels presentation and the undo timer.
 */
export type WorklistDismissalState =
  | { readonly kind: "idle" }
  | { readonly kind: "dismissing"; readonly listId: number; readonly shownListId: number | null }
  | { readonly kind: "undo"; readonly listId: number; readonly title: string; readonly expiresAt: number }
  | {
      readonly kind: "restoring";
      readonly listId: number;
      readonly title: string;
      readonly shownListId: number | null;
    };

const UNDO_WINDOW_MS = 10_000;
const IDLE: WorklistDismissalState = { kind: "idle" };

export interface WorklistDismissalOptions {
  readonly runtime: AppRuntime;
  readonly worklists: WorklistsStore;
  /** The list the page shows now. */
  readonly shownListId: () => number | null;
  /** Moves the page off the list it just dismissed. */
  readonly onDismissed: () => void;
  readonly onRestored: (listId: number) => void;
}

function failureMessage(failure: unknown, fallback: string): string {
  return failure instanceof ApiProblemError ? worklistFailureMessage(failure, fallback) : fallback;
}

export class WorklistDismissal {
  readonly #options: WorklistDismissalOptions;
  #state = $state.raw<WorklistDismissalState>(IDLE);
  #timer: AppExecution<void, never> | null = null;
  #active = true;

  constructor(options: WorklistDismissalOptions) {
    this.#options = options;
  }

  get state(): WorklistDismissalState {
    return this.#state;
  }

  /** Whether the undo still offers the list, so the page keeps it on screen though dismissed. */
  offersUndo(listId: number): boolean {
    const state = this.#state;
    return (state.kind === "undo" || state.kind === "restoring") && state.listId === listId;
  }

  dismiss(list: { readonly id: number; readonly title: string }): void {
    if (this.#state.kind === "dismissing" || this.#state.kind === "restoring") return;
    const { runtime, worklists, shownListId, onDismissed } = this.#options;
    const started = this.#enter({ kind: "dismissing", listId: list.id, shownListId: shownListId() });
    runtime.runCommand(
      worklists.dismissEffect(list.id).pipe(
        Effect.tap(() =>
          Effect.sync(() => {
            if (!this.#holds(started)) return;
            if (shownListId() !== started.shownListId) {
              this.#enter(IDLE);
              return;
            }
            this.#openUndo(list.id, list.title);
            onDismissed();
          }),
        ),
      ),
      {
        operation: "dismiss worklist",
        safeContext: {},
        onFailure: (failure) => {
          if (!this.#holds(started)) return;
          this.#enter(IDLE);
          showFlash(failureMessage(failure, "Could not dismiss the worklist."), { tone: "danger" });
        },
      },
    );
  }

  undo(): void {
    const state = this.#state;
    if (state.kind !== "undo") return;
    const { runtime, worklists, shownListId, onRestored } = this.#options;
    const { listId, title } = state;
    const started = this.#enter({ kind: "restoring", listId, title, shownListId: shownListId() });
    runtime.runCommand(
      worklists.undismissEffect(listId).pipe(
        Effect.tap(() =>
          Effect.sync(() => {
            if (!this.#holds(started)) return;
            this.#enter(IDLE);
            if (shownListId() === started.shownListId) onRestored(listId);
          }),
        ),
      ),
      {
        operation: "restore worklist",
        safeContext: {},
        onFailure: (failure) => {
          if (!this.#holds(started)) return;
          this.#openUndo(listId, title);
          showFlash(failureMessage(failure, "Could not restore the worklist."), { tone: "danger" });
        },
      },
    );
  }

  /** Ends presentation when the page goes away; requests already sent still finish in the store. */
  dispose(): void {
    this.#active = false;
    this.#enter(IDLE);
  }

  #holds(state: WorklistDismissalState): boolean {
    return this.#active && this.#state === state;
  }

  #enter<S extends WorklistDismissalState>(state: S): S {
    this.#timer?.interrupt();
    this.#timer = null;
    this.#state = state;
    return state;
  }

  #openUndo(listId: number, title: string): void {
    const undo = this.#enter({ kind: "undo", listId, title, expiresAt: Date.now() + UNDO_WINDOW_MS });
    this.#timer = this.#options.runtime.runCommand(
      Effect.sleep(UNDO_WINDOW_MS).pipe(
        Effect.andThen(
          Effect.sync(() => {
            if (this.#state === undo) this.#state = IDLE;
          }),
        ),
      ),
      { operation: "expire worklist undo", safeContext: {}, onFailure: () => {} },
    );
  }
}
