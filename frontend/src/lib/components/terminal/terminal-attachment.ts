import type { Attachment } from "svelte/attachments";
import { untrack } from "svelte";
import { Effect } from "effect";
import type { Scope } from "effect/Scope";

import type { AppRuntime, AppServices } from "../../app/runtime.js";

export interface TerminalAttachmentOptions<E> {
  readonly open: (node: HTMLElement) => Effect.Effect<void, E, AppServices | Scope>;
  readonly onFailure: (failure: E) => void;
}

export const terminalAttachment =
  <E>(runtime: AppRuntime, options: TerminalAttachmentOptions<E>): Attachment<HTMLElement> =>
  (node) => {
    const execution = untrack(() =>
      runtime.runCommand(Effect.scoped(options.open(node)), {
        operation: "terminal.session",
        safeContext: { surface: "workspace-terminal" },
        onFailure: options.onFailure,
      }),
    );
    return execution.interrupt;
  };
