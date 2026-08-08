import { Effect, FiberHandle } from "effect";
import type { AppRuntime } from "../app/runtime.js";
import { observeResize } from "../browser/observers.js";

type ContainerSize = "narrow" | "medium" | "wide";

let currentSize = $state<ContainerSize>("wide");

function classify(width: number): ContainerSize {
  if (width < 500) return "narrow";
  if (width < 1100) return "medium";
  return "wide";
}

export function initContainerObserver(runtime: AppRuntime, el: HTMLElement): () => void {
  function apply(size: ContainerSize): void {
    currentSize = size;
    el.classList.remove("container-narrow", "container-medium");
    if (size === "narrow") {
      el.classList.add("container-narrow");
    } else if (size === "medium") {
      el.classList.add("container-medium");
    }
  }

  const execution = runtime.runCommand(
    Effect.scoped(
      Effect.gen(function* () {
        const runDebounce = yield* FiberHandle.makeRuntime<never>();
        yield* Effect.sync(() => apply(classify(el.clientWidth)));
        yield* observeResize(el, (entries) => {
          const entry = entries[0];
          if (!entry) return;
          const width = entry.contentRect.width;
          runDebounce(Effect.sleep("100 millis").pipe(Effect.andThen(Effect.sync(() => apply(classify(width))))));
        });
        return yield* Effect.never;
      }),
    ),
    { operation: "observe app container width", safeContext: {}, onFailure: () => {} },
  );
  return execution.interrupt;
}

export function getContainerSize(): ContainerSize {
  return currentSize;
}

export function isNarrow(): boolean {
  return currentSize === "narrow";
}
