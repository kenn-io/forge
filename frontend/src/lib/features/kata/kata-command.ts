import type { Effect } from "effect/Effect";
import type { AppServices } from "../../app/runtime.js";

export type KataCommand<A, E = never> = Effect<A, E, AppServices>;
