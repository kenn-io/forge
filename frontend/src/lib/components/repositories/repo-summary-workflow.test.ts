import { afterEach, assert, it, vi } from "@effect/vitest";
import { Deferred, Effect, Fiber, Layer, Option } from "effect";
import { TestClock } from "effect/testing";
import { GeneratedApiLive } from "../../api/generated-api.js";
import { RepoSummaryWorkflow, RepoSummaryWorkflowLive } from "./repo-summary-workflow.js";

const RepoSummaryWorkflowTest = Layer.provide(RepoSummaryWorkflowLive, GeneratedApiLive);

afterEach(() => {
  vi.unstubAllGlobals();
});

it.layer(RepoSummaryWorkflowTest)("repository issue presenter release", (it) => {
  it.effect("does not hold the registry lock while presenting an accepted request", () =>
    Effect.gen(function* () {
      vi.stubGlobal("fetch", () => new Promise<Response>(() => undefined));
      const workflow = yield* RepoSummaryWorkflow;
      yield* workflow.claimIssuePresenter("presenter", (state) =>
        state.kind === "pending"
          ? workflow.releaseIssuePresenter("presenter").pipe(Effect.as(false))
          : Effect.succeed(false),
      );

      const completion = yield* workflow
        .createIssue({
          ref: {
            provider: "github",
            platformHost: "github.com",
            owner: "acme",
            name: "widgets",
            repoPath: "acme/widgets",
          },
          title: "Do not deadlock",
          body: "",
        })
        .pipe(Effect.timeoutOption("1 second"), Effect.forkChild);
      yield* Effect.yieldNow;
      yield* TestClock.adjust("1 second");

      assert.isTrue(Option.isSome(yield* Fiber.join(completion)));
    }),
  );
});

it.layer(RepoSummaryWorkflowTest)("repository issue presenter replacement", (it) => {
  it.effect("interrupts an old delivery before a replacement presenter adopts it", () =>
    Effect.gen(function* () {
      vi.stubGlobal(
        "fetch",
        () =>
          new Response(
            JSON.stringify({
              id: 42,
              number: 42,
              title: "Retained issue",
              body: "",
              state: "open",
              html_url: "https://example.test/issues/42",
              author: { login: "maintainer" },
              labels: [],
              assignees: [],
              created_at: "2026-08-05T00:00:00Z",
              updated_at: "2026-08-05T00:00:00Z",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
      );
      const workflow = yield* RepoSummaryWorkflow;
      const oldDeliveryStarted = yield* Deferred.make<void>();
      const oldDeliveryInterrupted = yield* Deferred.make<void>();
      const replacementDelivered = yield* Deferred.make<void>();
      yield* workflow.claimIssuePresenter("old", (state) =>
        state.kind === "succeeded"
          ? Deferred.succeed(oldDeliveryStarted, undefined).pipe(
              Effect.andThen(Effect.never),
              Effect.onInterrupt(() => Deferred.succeed(oldDeliveryInterrupted, undefined)),
            )
          : Effect.succeed(false),
      );

      yield* workflow.createIssue({
        ref: {
          provider: "github",
          platformHost: "github.com",
          owner: "acme",
          name: "widgets",
          repoPath: "acme/widgets",
        },
        title: "Retained issue",
        body: "",
      });
      yield* Deferred.await(oldDeliveryStarted);
      yield* workflow.claimIssuePresenter("replacement", (state) =>
        state.kind === "succeeded"
          ? Deferred.succeed(replacementDelivered, undefined).pipe(Effect.as(true))
          : Effect.succeed(false),
      );
      yield* Deferred.await(replacementDelivered);
      yield* Effect.yieldNow;

      assert.isTrue(Option.isSome(yield* Deferred.poll(oldDeliveryInterrupted)));
    }),
  );
});

it.layer(RepoSummaryWorkflowTest)("repository issue queue backpressure", (it) => {
  it.effect("keeps the registry available while queue admission waits for capacity", () =>
    Effect.gen(function* () {
      let providerRequests = 0;
      vi.stubGlobal("fetch", () => {
        providerRequests += 1;
        return new Promise<Response>(() => undefined);
      });
      const workflow = yield* RepoSummaryWorkflow;
      yield* workflow.claimIssuePresenter("presenter", () => Effect.succeed(false));
      const request = (index: number) =>
        workflow.createIssue({
          ref: {
            provider: "github",
            platformHost: "github.com",
            owner: "acme",
            name: `widgets-${index}`,
            repoPath: `acme/widgets-${index}`,
          },
          title: `Queued issue ${index}`,
          body: "",
        });

      yield* request(0);
      while (providerRequests === 0) yield* Effect.yieldNow;
      for (let index = 1; index <= 64; index += 1) yield* request(index);
      const blockedAdmission = yield* request(65).pipe(Effect.forkChild);
      yield* Effect.yieldNow;
      assert.isUndefined(blockedAdmission.pollUnsafe());

      const release = yield* workflow
        .releaseIssuePresenter("presenter")
        .pipe(Effect.timeoutOption("1 second"), Effect.forkChild);
      yield* Effect.yieldNow;
      yield* TestClock.adjust("1 second");

      assert.isTrue(Option.isSome(yield* Fiber.join(release)));
    }),
  );
});
