import { describe, expect, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Layer } from "effect";
import { makeGeneratedApiLayer } from "../../api/generated-api.js";
import { createRuntimeClient } from "../../api/runtime.js";
import type { KataWorkspaceSnapshotResponse } from "../../api/kata/snapshot.js";
import { KataWorkflowLive } from "./kata-workflow.js";
import {
  createKataWorkspaceAuthorityController,
  createKataWorkspaceAuthorityOwner,
} from "./kataWorkspaceAuthorityController.svelte.js";

describe("Kata workspace authority ownership", () => {
  it("gives replacement workspace mounts independent authority owners", () => {
    expect(createKataWorkspaceAuthorityOwner()).not.toBe(createKataWorkspaceAuthorityOwner());
  });

  it.effect("does not publish a snapshot after its mount starts teardown", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const release = yield* Deferred.make<KataWorkspaceSnapshotResponse>();
        const accepted: KataWorkspaceSnapshotResponse[] = [];
        const controller = createKataWorkspaceAuthorityController({
          owner: createKataWorkspaceAuthorityOwner(),
          loadSnapshot: () => Deferred.await(release),
          onSnapshotAccepted: (snapshot) =>
            Effect.sync(() => {
              accepted.push(snapshot);
            }),
        });
        const fetchImpl: typeof fetch = () =>
          Promise.resolve(
            new Response(new ReadableStream<Uint8Array>(), {
              status: 200,
              headers: { "Content-Type": "text/event-stream" },
            }),
          );
        const layer = Layer.merge(KataWorkflowLive, makeGeneratedApiLayer(createRuntimeClient(fetchImpl)));
        const load = yield* Effect.forkChild(
          controller
            .load({
              intent: { daemon_id: "home", scope: "global", authority: "open" },
              presentation: { text: "", owner: "", label: "" },
            })
            .pipe(Effect.provide(layer)),
        );
        yield* Effect.yieldNow;

        yield* controller.dispose().pipe(Effect.provide(layer));
        yield* Deferred.succeed(release, {
          server_instance_id: "server-a",
          daemon_id: "home",
          intent: { scope: "global", authority: "open" },
          generation: 1,
          invalidation_epoch: 1,
          event_cursor: 1,
          fetched_at: "2026-08-04T10:00:00Z",
          projects: [],
          member_issue_uids: [],
          issues: [],
          enrichment: {},
        });
        yield* Fiber.await(load);

        expect(accepted).toEqual([]);
      }),
    ),
  );

  it.effect("runs accepted snapshot work before completing the authority load", () =>
    Effect.scoped(
      Effect.gen(function* () {
        let accepted = 0;
        const controller = createKataWorkspaceAuthorityController({
          owner: createKataWorkspaceAuthorityOwner(),
          loadSnapshot: () =>
            Effect.succeed({
              server_instance_id: "server-a",
              daemon_id: "home",
              intent: { scope: "global", authority: "open" },
              generation: 1,
              invalidation_epoch: 1,
              event_cursor: 1,
              fetched_at: "2026-08-04T10:00:00Z",
              projects: [],
              member_issue_uids: [],
              issues: [],
              enrichment: {},
            }),
          onSnapshotAccepted: () =>
            Effect.sync(() => {
              accepted += 1;
            }),
        });
        const fetchImpl: typeof fetch = () =>
          Promise.resolve(
            new Response(new ReadableStream<Uint8Array>(), {
              status: 200,
              headers: { "Content-Type": "text/event-stream" },
            }),
          );
        const layer = Layer.merge(KataWorkflowLive, makeGeneratedApiLayer(createRuntimeClient(fetchImpl)));

        yield* controller
          .load({
            intent: { daemon_id: "home", scope: "global", authority: "open" },
            presentation: { text: "", owner: "", label: "" },
          })
          .pipe(Effect.provide(layer));

        expect(accepted).toBe(1);
        yield* controller.dispose().pipe(Effect.provide(layer));
      }),
    ),
  );
});
