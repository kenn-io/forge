import { assert, it } from "@effect/vitest";
import { Cause, Deferred, Effect, Exit, Fiber, Option, Ref } from "effect";
import { DocsRequestError } from "../api/docs/api.js";
import { makeDocsWorkflow } from "./docs-workflow.js";

function docsFailure(operation: string): DocsRequestError {
  return DocsRequestError.make({
    operation,
    message: `${operation} failed`,
    status: 0,
    cause: new Error(`${operation} failed`),
  });
}

it.effect("recovers a Docs mutation whose authoritative snapshot proves the response was lost", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const outcome = yield* workflow.reconcileMutation(
        { resource: '["tree","notes"]', intent: '["create","created.md"]' },
        "create Docs document",
        Effect.fail(docsFailure("create Docs document")),
        Effect.succeed(["README.md", "created.md"]),
        (paths) => (paths.includes("created.md") ? Option.some("created.md") : Option.none()),
      );

      assert.strictEqual(outcome.result, "created.md");
      assert.deepStrictEqual(outcome.snapshot, ["README.md", "created.md"]);
      assert.isTrue(outcome.recovered);
    }),
  ),
);

it.effect("preserves an explicit Docs rejection even when the snapshot matches recovery evidence", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const rejection = DocsRequestError.make({
        operation: "create Docs document",
        message: "A file with that name already exists",
        status: 409,
        code: "already_exists",
        cause: new Error("A file with that name already exists"),
      });
      const failure = yield* workflow
        .reconcileMutation(
          { resource: '["tree","notes"]', intent: '["create","README.md"]' },
          "create Docs document",
          Effect.fail(rejection),
          Effect.succeed(["README.md"]),
          (paths) => (paths.includes("README.md") ? Option.some("README.md") : Option.none()),
        )
        .pipe(Effect.flip);

      assert.strictEqual(failure, rejection);
    }),
  ),
);

it.effect("reports uncertain Docs mutation state when the authoritative snapshot also fails", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const failure = yield* workflow
        .reconcileMutation(
          { resource: '["tree","notes"]', intent: '["create","created.md"]' },
          "create Docs document",
          Effect.fail(docsFailure("create Docs document")),
          Effect.fail(docsFailure("load Docs tree")),
          () => Option.none<string>(),
        )
        .pipe(Effect.flip);

      assert.strictEqual(failure._tag, "DocsMutationStateUncertainError");
    }),
  ),
);

it.effect("reconciles retained Docs uncertainty before resubmitting the same intent", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const requests = yield* Ref.make(0);
      const reconciliations = yield* Ref.make(0);
      const request = Ref.updateAndGet(requests, (count) => count + 1).pipe(
        Effect.andThen(Effect.fail(docsFailure("create Docs document"))),
      );
      const reconcile = Ref.updateAndGet(reconciliations, (count) => count + 1).pipe(
        Effect.flatMap((attempt) =>
          attempt === 1 ? Effect.fail(docsFailure("load Docs tree")) : Effect.succeed(["README.md", "created.md"]),
        ),
      );
      const identity = { resource: '["tree","notes"]', intent: '["create","created.md"]' };

      yield* workflow
        .reconcileMutation(identity, "create Docs document", request, reconcile, (paths) =>
          paths.includes("created.md") ? Option.some("created.md") : Option.none(),
        )
        .pipe(Effect.exit);
      const outcome = yield* workflow.reconcileMutation(
        identity,
        "create Docs document",
        request,
        reconcile,
        (paths) => (paths.includes("created.md") ? Option.some("created.md") : Option.none()),
      );

      assert.strictEqual(outcome.result, "created.md");
      assert.strictEqual(yield* Ref.get(requests), 1);
    }),
  ),
);

it.effect("does not let a same-resource presentation read interrupt accepted reconciliation", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const identity = { resource: '["document","notes","README.md"]', intent: '["save","updated"]' };
      const mutation = yield* workflow
        .reconcileMutation(
          identity,
          "save Docs document",
          Effect.fail(docsFailure("save Docs document")),
          workflow.read(
            "docs-mutations",
            { lane: "document", resource: identity.resource },
            Deferred.succeed(reconciliationStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseReconciliation)),
              Effect.as("updated"),
            ),
          ),
          (content) => (content === "updated" ? Option.some(undefined) : Option.none()),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(reconciliationStarted);

      const replacement = yield* workflow.read(
        "replacement-view",
        { lane: "document", resource: identity.resource },
        Effect.succeed("updated"),
      );
      yield* Deferred.succeed(releaseReconciliation, undefined);
      const outcome = yield* Fiber.join(mutation);

      assert.strictEqual(replacement, "updated");
      assert.isTrue(outcome.recovered);
    }),
  ),
);

it.effect("interrupts a superseded Docs read and publishes only its replacement", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStarted = yield* Deferred.make<void>();
      const published = yield* Ref.make<ReadonlyArray<string>>([]);
      const first = yield* workflow
        .read(
          "docs-view",
          { lane: "document", resource: '["document","notes","README.md"]' },
          Deferred.succeed(firstStarted, undefined).pipe(
            Effect.andThen(Effect.never),
            Effect.tap((value: string) => Ref.update(published, (values) => [...values, value])),
          ),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(firstStarted);

      yield* workflow
        .read(
          "docs-view",
          { lane: "document", resource: '["document","engineering","index.md"]' },
          Effect.succeed("replacement").pipe(
            Effect.tap((value) => Ref.update(published, (values) => [...values, value])),
          ),
        )
        .pipe(Effect.forkChild, Effect.flatMap(Fiber.join));

      const firstExit = yield* Fiber.await(first);
      assert.isTrue(Exit.isFailure(firstExit) && Cause.hasInterruptsOnly(firstExit.cause));
      assert.deepStrictEqual(yield* Ref.get(published), ["replacement"]);
    }),
  ),
);

it.effect("uses one latest-wins key for the same Docs resource across replacement owners", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStarted = yield* Deferred.make<void>();
      const first = yield* workflow
        .read(
          "first-view",
          { lane: "tree", resource: '["tree","notes"]' },
          Deferred.succeed(firstStarted, undefined).pipe(Effect.andThen(Effect.never)),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(firstStarted);

      const replacement = yield* workflow
        .read("replacement-view", { lane: "tree", resource: '["tree","notes"]' }, Effect.succeed("fresh tree"))
        .pipe(Effect.forkChild);
      yield* Effect.yieldNow;
      const replacementExit = yield* Effect.sync(() => replacement.pollUnsafe());
      const firstExit = yield* Effect.sync(() => first.pollUnsafe());

      assert.isTrue(replacementExit !== undefined && Exit.isSuccess(replacementExit));
      if (replacementExit !== undefined && Exit.isSuccess(replacementExit)) {
        assert.strictEqual(replacementExit.value, "fresh tree");
      }
      assert.isTrue(firstExit !== undefined && Exit.isFailure(firstExit) && Cause.hasInterruptsOnly(firstExit.cause));
    }),
  ),
);

it.effect("does not let a released Docs owner stop its replacement resource read", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStarted = yield* Deferred.make<void>();
      const replacementStarted = yield* Deferred.make<void>();
      const releaseReplacement = yield* Deferred.make<void>();
      const first = yield* workflow
        .read(
          "first-view",
          { lane: "document", resource: '["document","notes","README.md"]' },
          Deferred.succeed(firstStarted, undefined).pipe(Effect.andThen(Effect.never)),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(firstStarted);
      const replacement = yield* workflow
        .read(
          "replacement-view",
          { lane: "document", resource: '["document","notes","README.md"]' },
          Deferred.succeed(replacementStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseReplacement)),
            Effect.as("fresh document"),
          ),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(replacementStarted);

      yield* workflow.stop("first-view");
      yield* Deferred.succeed(releaseReplacement, undefined);

      assert.strictEqual(yield* Fiber.join(replacement), "fresh document");
      const firstExit = yield* Fiber.await(first);
      assert.isTrue(Exit.isFailure(firstExit) && Cause.hasInterruptsOnly(firstExit.cause));
    }),
  ),
);

it.effect("runs accepted Docs mutations once in submission order", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStarted = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      const observed = yield* Ref.make<ReadonlyArray<string>>([]);

      const first = yield* workflow
        .mutate(
          "workspace",
          "first-session",
          Deferred.succeed(firstStarted, undefined).pipe(
            Effect.andThen(Ref.update(observed, (values) => [...values, "write"])),
            Effect.andThen(Deferred.await(releaseFirst)),
          ),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(firstStarted);
      const second = yield* workflow
        .mutate(
          "workspace",
          "first-session",
          Ref.update(observed, (values) => [...values, "publish"]),
        )
        .pipe(Effect.forkChild);
      yield* Effect.yieldNow;

      assert.deepStrictEqual(yield* Ref.get(observed), ["write"]);
      yield* Deferred.succeed(releaseFirst, undefined);
      yield* Fiber.join(first);
      yield* Fiber.join(second);
      assert.deepStrictEqual(yield* Ref.get(observed), ["write", "publish"]);
    }),
  ),
);

it.effect("delivers an accepted mutation refresh to the replacement presenter", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const mutationStarted = yield* Deferred.make<void>();
      const releaseMutation = yield* Deferred.make<void>();
      const firstPresentations = yield* Ref.make<ReadonlyArray<string>>([]);
      const replacementPresentations = yield* Ref.make<ReadonlyArray<string>>([]);

      yield* workflow.claimPresenter(
        "workspace",
        "first-session",
        Ref.update(firstPresentations, (values) => [...values, "refresh"]),
      );
      const mutation = yield* workflow
        .mutate(
          "workspace",
          "first-session",
          Deferred.succeed(mutationStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseMutation)),
            Effect.as("saved"),
          ),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(mutationStarted);
      yield* workflow.releasePresenter("workspace", "first-session");
      yield* workflow.claimPresenter(
        "workspace",
        "replacement-session",
        Ref.update(replacementPresentations, (values) => [...values, "refresh"]),
      );
      yield* Deferred.succeed(releaseMutation, undefined);
      yield* Fiber.join(mutation);
      yield* workflow.present(
        "workspace",
        "first-session",
        Ref.update(firstPresentations, (values) => [...values, "saved"]),
      );

      assert.deepStrictEqual(yield* Ref.get(firstPresentations), []);
      assert.deepStrictEqual(yield* Ref.get(replacementPresentations), ["refresh"]);
    }),
  ),
);

it.effect("retains a completed mutation refresh until a presenter claims the surface", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const presentations = yield* Ref.make<ReadonlyArray<string>>([]);

      yield* workflow.mutate("workspace", "departed-session", Effect.void);
      yield* workflow.claimPresenter(
        "workspace",
        "replacement-session",
        Ref.update(presentations, (values) => [...values, "refresh"]),
      );

      assert.deepStrictEqual(yield* Ref.get(presentations), ["refresh"]);
    }),
  ),
);

it.effect("moves an unfinished Docs publish outcome to the remounted folder surface", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const requestStarted = yield* Deferred.make<void>();
      const releaseRequest = yield* Deferred.make<void>();
      const firstStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const secondStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const record = (states: Ref.Ref<ReadonlyArray<string>>) => (state: { readonly kind: string }) =>
        Ref.update(states, (values) => [...values, state.kind]);

      yield* workflow.claimPublisher("notes", "first-session", record(firstStates));
      const publish = yield* workflow
        .publish({
          folderID: "notes",
          sessionID: "first-session",
          message: "docs: publish notes",
          request: Deferred.succeed(requestStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseRequest)),
            Effect.andThen(
              Effect.fail(
                DocsRequestError.make({
                  operation: "publish Docs",
                  message: "push failed",
                  status: 500,
                  code: "push_failed_after_commit",
                  commit: "abcdef1234567890",
                  cause: new Error("response unavailable"),
                }),
              ),
            ),
          ),
        })
        .pipe(Effect.forkChild);
      yield* Deferred.await(requestStarted);
      yield* workflow.releasePublisher("first-session");
      yield* workflow.claimPublisher("notes", "second-session", record(secondStates));
      yield* Deferred.succeed(releaseRequest, undefined);
      yield* Fiber.join(publish);

      assert.deepStrictEqual(yield* Ref.get(firstStates), ["pending"]);
      assert.deepStrictEqual(yield* Ref.get(secondStates), ["pending", "failed"]);
    }),
  ),
);

it.effect("does not deliver a completed Docs publish success to a later dialog session", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const secondStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const record = (states: Ref.Ref<ReadonlyArray<string>>) => (state: { readonly kind: string }) =>
        Ref.update(states, (values) => [...values, state.kind]);

      yield* workflow.claimPublisher("notes", "first-session", record(firstStates));
      yield* workflow.publish({
        folderID: "notes",
        sessionID: "first-session",
        message: "docs: publish notes",
        request: Effect.succeed({
          commit: "abcdef1234567890",
          short_commit: "abcdef1",
          branch: "main",
          pushed: true,
          files: [],
        }),
      });
      yield* workflow.releasePublisher("first-session");
      yield* workflow.claimPublisher("notes", "second-session", record(secondStates));

      assert.deepStrictEqual(yield* Ref.get(firstStates), ["pending", "succeeded"]);
      assert.deepStrictEqual(yield* Ref.get(secondStates), []);
    }),
  ),
);

it.effect("delivers an accepted publish to the replacement surface even when its submitter starts late", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const secondStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const requests = yield* Ref.make(0);
      const record = (states: Ref.Ref<ReadonlyArray<string>>) => (state: { readonly kind: string }) =>
        Ref.update(states, (values) => [...values, state.kind]);

      yield* workflow.claimPublisher("notes", "first-session", record(firstStates));
      yield* workflow.claimPublisher("notes", "second-session", record(secondStates));
      yield* workflow.publish({
        folderID: "notes",
        sessionID: "first-session",
        message: "docs: publish notes",
        request: Ref.update(requests, (count) => count + 1).pipe(
          Effect.andThen(
            Effect.fail(
              DocsRequestError.make({
                operation: "publish Docs",
                message: "push failed",
                status: 500,
                code: "push_failed_after_commit",
                commit: "abcdef1234567890",
                cause: new Error("response unavailable"),
              }),
            ),
          ),
        ),
      });

      assert.deepStrictEqual(yield* Ref.get(firstStates), []);
      assert.deepStrictEqual(yield* Ref.get(secondStates), ["pending", "failed"]);
      assert.strictEqual(yield* Ref.get(requests), 1);
    }),
  ),
);
