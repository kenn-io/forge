import type { Effect as EffectType } from "effect/Effect";
import type { AppServices } from "../../app/runtime.js";

export type KeyboardHandlerResult = void | EffectType<void, unknown, AppServices>;

export interface KeySpec {
  key: string;
  ctrlOrMeta?: boolean;
  shift?: boolean;
  alt?: boolean;
}

export interface ModalFrameAction {
  id: string;
  label: string;
  binding: KeySpec | KeySpec[] | null;
  priority?: number;
  when?: (ctx: unknown) => boolean;
  handler: (ctx: unknown) => KeyboardHandlerResult;
}
