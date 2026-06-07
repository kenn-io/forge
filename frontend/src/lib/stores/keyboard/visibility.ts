import type { Action, Context } from "./types.js";

export function isActionVisible(action: Action, ctx: Context): boolean {
  return (action.visible ?? action.when)(ctx);
}
