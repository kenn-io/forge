import { Effect } from "effect";
import type * as Duration from "effect/Duration";

import { DocumentVisibility } from "../browser/document-visibility.js";

export interface PollWhileVisibleOptions {
  /**
   * Run `pollOnce` before the first wait, like `Stream.fromEffectSchedule`.
   * Defaults to false, matching `Stream.fromSchedule(Schedule.spaced(...))`.
   */
  readonly immediate?: boolean;
}

/**
 * Repeats `pollOnce` every `interval` while the document is visible and
 * suspends the timer entirely while it is hidden. When the document becomes
 * visible again the poll runs immediately and the interval restarts from
 * that point. Each poll finishes before the next wait starts, so runs never
 * overlap. The shared visibility source lets every timer-based poll in the
 * app pause together; event-driven refreshes are unaffected.
 *
 * `interval` may also be an Effect that completes when the next poll is
 * due, for pollers whose cadence changes at runtime or that can be woken
 * early. It is re-run for every wait, and a hidden document interrupts it.
 */
export function pollWhileVisible<E, R, WaitR = never>(
  pollOnce: Effect.Effect<void, E, R>,
  interval: Duration.Input | Effect.Effect<void, never, WaitR>,
  options: PollWhileVisibleOptions = {},
): Effect.Effect<void, E, R | WaitR> {
  const wait: Effect.Effect<void, never, WaitR> = Effect.isEffect(interval) ? interval : Effect.sleep(interval);
  return Effect.gen(function* () {
    const visibility = yield* DocumentVisibility;
    let runBeforeWait = options.immediate === true;
    while (true) {
      if (!visibility.visible()) {
        yield* visibility.untilVisible;
        runBeforeWait = true;
      }
      if (runBeforeWait) {
        runBeforeWait = false;
        yield* pollOnce;
        continue;
      }
      const hiddenDuringWait = yield* Effect.raceFirst(Effect.as(wait, false), Effect.as(visibility.untilHidden, true));
      if (hiddenDuringWait) {
        // Latch the refresh now: the document may already be visible again
        // by the time this fiber resumes, and that transition still owes an
        // immediate poll.
        runBeforeWait = true;
        continue;
      }
      yield* pollOnce;
    }
  });
}
