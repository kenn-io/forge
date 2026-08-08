import { assert, it } from "@effect/vitest";
import { Effect, Schema } from "effect";
import { InvalidExternalPayload } from "../api/effect-errors.js";
import { LocalStorage, LocalStorageLive } from "./storage.js";

it.layer(LocalStorageLive)("LocalStorage decoding", (it) => {
  it.effect("preserves malformed JSON as a parse failure", () =>
    Effect.gen(function* () {
      localStorage.setItem("malformed", "{");
      const storage = yield* LocalStorage;

      const failure = yield* Effect.flip(storage.get("malformed", Schema.String));

      assert.instanceOf(failure, InvalidExternalPayload);
      assert.strictEqual(failure.operation, "localStorage.parse");
    }),
  );
});
