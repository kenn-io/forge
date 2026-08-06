import { assert, describe, it } from "@effect/vitest";
import { Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { fleetWorkspaceDiffWatch, shouldRetryFleetDiffWatch } from "./fleet-diff-watch.js";

describe("shouldRetryFleetDiffWatch", () => {
  it.each([
    { status: 404, retry: false },
    { status: 405, retry: false },
    { status: 408, retry: true },
    { status: 409, retry: true },
    { status: 425, retry: true },
    { status: 429, retry: true },
    { status: 500, retry: true },
    { status: 501, retry: false },
    { status: 503, retry: true },
  ])("returns $retry for HTTP $status", ({ status, retry }) => {
    assert.strictEqual(shouldRetryFleetDiffWatch(status), retry);
  });
});

describe("fleetWorkspaceDiffWatch", () => {
  it.effect("carries the latest version forward and publishes changed snapshots once", () =>
    Effect.gen(function* () {
      const requestedVersions = yield* Ref.make<ReadonlyArray<string>>([]);
      const changedVersions = yield* Ref.make<ReadonlyArray<string>>([]);
      const responses = yield* Ref.make<ReadonlyArray<{ status: number; body: unknown }>>([
        { status: 200, body: { changed: true, version: "fleet:1" } },
        { status: 200, body: { changed: false, version: "fleet:2" } },
        { status: 501, body: {} },
      ]);

      yield* fleetWorkspaceDiffWatch({
        workspaceId: "ws-1",
        hostKey: "member",
        request: (_workspaceId, _hostKey, version) =>
          Ref.modify(responses, (remaining) => {
            const [response, ...rest] = remaining;
            return [response ?? { status: 501, body: {} }, rest];
          }).pipe(Effect.tap(() => Ref.update(requestedVersions, (versions) => [...versions, version]))),
        onChanged: (version) => Ref.update(changedVersions, (versions) => [...versions, version]),
      });

      assert.deepStrictEqual(yield* Ref.get(requestedVersions), ["", "fleet:1", "fleet:2"]);
      assert.deepStrictEqual(yield* Ref.get(changedVersions), ["fleet:1"]);
    }),
  );

  it.effect("retries a transient status before continuing from the recovered version", () =>
    Effect.gen(function* () {
      const requestedVersions = yield* Ref.make<ReadonlyArray<string>>([]);
      const responses = yield* Ref.make<ReadonlyArray<{ status: number; body: unknown }>>([
        { status: 409, body: {} },
        { status: 200, body: { changed: true, version: "fleet:ready" } },
        { status: 501, body: {} },
      ]);

      const fiber = yield* Effect.forkChild(
        fleetWorkspaceDiffWatch({
          workspaceId: "ws-1",
          hostKey: "member",
          request: (_workspaceId, _hostKey, version) =>
            Ref.modify(responses, (remaining) => {
              const [response, ...rest] = remaining;
              return [response ?? { status: 501, body: {} }, rest];
            }).pipe(Effect.tap(() => Ref.update(requestedVersions, (versions) => [...versions, version]))),
          onChanged: () => Effect.void,
        }),
      );

      yield* Effect.yieldNow;
      yield* TestClock.adjust("2 seconds");
      yield* Fiber.await(fiber);

      assert.deepStrictEqual(yield* Ref.get(requestedVersions), ["", "", "fleet:ready"]);
    }),
  );
});
