import { Context, Effect } from "effect";

/**
 * Page Visibility as an Effect service. `visible` is a synchronous read;
 * `untilHidden` and `untilVisible` resolve on the next transition into that
 * state (or immediately when already there) and remove their listener when
 * interrupted.
 */
export interface DocumentVisibilitySource {
  readonly visible: () => boolean;
  readonly untilHidden: Effect.Effect<void>;
  readonly untilVisible: Effect.Effect<void>;
}

function awaitVisibilityState(target: DocumentVisibilityState): Effect.Effect<void> {
  return Effect.suspend(() => {
    if (document.visibilityState === target) return Effect.void;
    // Effect.callback runs the returned cleanup only on interruption, so a
    // waiter that completes normally must detach its own listener first or
    // every hide/show cycle leaks one.
    return Effect.callback<void>((resume) => {
      const handleVisibilityChange = () => {
        if (document.visibilityState !== target) return;
        document.removeEventListener("visibilitychange", handleVisibilityChange);
        resume(Effect.void);
      };
      document.addEventListener("visibilitychange", handleVisibilityChange);
      return Effect.sync(() => document.removeEventListener("visibilitychange", handleVisibilityChange));
    });
  });
}

// Workflows and their unit tests also run where no document exists (Node
// test projects, worker contexts). Without a document nothing can be hidden,
// so polling behaves exactly as an always-visible page.
const alwaysVisible: DocumentVisibilitySource = {
  visible: () => true,
  untilHidden: Effect.never,
  untilVisible: Effect.void,
};

const browserDocumentVisibility: DocumentVisibilitySource = {
  visible: () => document.visibilityState !== "hidden",
  untilHidden: awaitVisibilityState("hidden"),
  untilVisible: awaitVisibilityState("visible"),
};

export const DocumentVisibility = Context.Reference<DocumentVisibilitySource>("kenn-forge/browser/DocumentVisibility", {
  defaultValue: () => (typeof document === "undefined" ? alwaysVisible : browserDocumentVisibility),
});

/**
 * A hand-driven visibility source for tests: flip it with `hide()` and
 * `show()` and every waiter observes the transition.
 */
export function makeManualDocumentVisibility(initiallyVisible = true): DocumentVisibilitySource & {
  readonly hide: () => void;
  readonly show: () => void;
} {
  let visible = initiallyVisible;
  const hiddenWaiters = new Set<() => void>();
  const visibleWaiters = new Set<() => void>();
  const flush = (waiters: Set<() => void>) => {
    for (const resume of [...waiters]) resume();
    waiters.clear();
  };
  const awaitState = (target: boolean, waiters: Set<() => void>) =>
    Effect.suspend(() => {
      if (visible === target) return Effect.void;
      return Effect.callback<void>((resume) => {
        const wake = () => {
          waiters.delete(wake);
          resume(Effect.void);
        };
        waiters.add(wake);
        return Effect.sync(() => {
          waiters.delete(wake);
        });
      });
    });
  return {
    visible: () => visible,
    untilHidden: awaitState(false, hiddenWaiters),
    untilVisible: awaitState(true, visibleWaiters),
    hide: () => {
      if (!visible) return;
      visible = false;
      flush(hiddenWaiters);
    },
    show: () => {
      if (visible) return;
      visible = true;
      flush(visibleWaiters);
    },
  };
}
