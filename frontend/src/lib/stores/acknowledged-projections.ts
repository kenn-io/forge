import { Effect } from "effect";
import type { ProviderMutationService } from "./ordered-mutations.js";

type Projection = { readonly key: string; readonly confirmed: Effect.Effect<void> };

export function createAcknowledgedProjections() {
  const saved = new Map<string, { readonly tick: number; readonly confirmed: Effect.Effect<void> }>();
  return {
    record(key: string, tick: number, confirmed: Effect.Effect<void>) {
      saved.set(key, { tick, confirmed });
    },
    clear() {
      saved.clear();
    },
    rebase(
      mutations: ProviderMutationService,
      requestTick: number,
      install: Effect.Effect<boolean>,
      entries: readonly Projection[],
    ) {
      const rebased: Projection[] = [];
      return mutations.rebaseAll(
        install.pipe(
          Effect.flatMap((applied) => {
            if (!applied) return Effect.succeed(false);
            return Effect.forEach(entries, (entry) =>
              Effect.gen(function* () {
                const acknowledged = saved.get(entry.key);
                if (acknowledged !== undefined && acknowledged.tick > requestTick) {
                  rebased.push({ key: entry.key, confirmed: acknowledged.confirmed });
                  yield* acknowledged.confirmed;
                } else {
                  saved.delete(entry.key);
                  rebased.push(entry);
                }
              }),
            ).pipe(Effect.as(true));
          }),
        ),
        rebased,
      );
    },
  };
}
