import { assert, it } from "@effect/vitest";
import { Deferred, Effect } from "effect";

import { makeAppRuntime } from "../../app/runtime.js";
import { terminalAttachment } from "./terminal-attachment.js";

it.effect("releases the terminal scope when its element detaches", () =>
  Effect.gen(function* () {
    const runtime = makeAppRuntime();
    const acquired = yield* Deferred.make<void>();
    const released = yield* Deferred.make<void>();
    const attach = terminalAttachment(runtime, {
      open: () =>
        Effect.acquireRelease(Deferred.succeed(acquired, undefined), () => Deferred.succeed(released, undefined)).pipe(
          Effect.andThen(Effect.never),
        ),
      onFailure: () => {},
    });

    const detach = attach(document.createElement("div"));
    yield* Deferred.await(acquired);
    detach?.();
    yield* Deferred.await(released);
    yield* runtime.disposeEffect;

    assert.isTrue(true);
  }),
);
