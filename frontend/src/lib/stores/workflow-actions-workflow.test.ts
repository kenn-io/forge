import { assert, it, vi } from "@effect/vitest";
import { Effect, Fiber, Layer } from "effect";
import { TestClock } from "effect/testing";

import type { components } from "../api/generated/schema.js";
import { makeGeneratedApiLayer, type GeneratedClient } from "../api/generated-api.js";
import type { ProviderRouteRef } from "../api/provider-routes.js";
import {
  WorkflowActionsWorkflow,
  WorkflowActionsWorkflowLive,
  workflowRepositoryKey,
} from "./workflow-actions-workflow.js";

type WorkflowCatalog = components["schemas"]["WorkflowCatalogResponse"];
type WorkflowDispatchResponse = components["schemas"]["WorkflowDispatchResponse"];
type WorkflowJobs = components["schemas"]["WorkflowJobsResponse"];
type WorkflowRun = components["schemas"]["WorkflowRunResponse"];
type WorkflowRuns = components["schemas"]["WorkflowRunsResponse"];

const github: ProviderRouteRef = {
  provider: "gh",
  platformHost: "github.com",
  owner: "octo",
  name: "repo",
  repoPath: "/octo//repo/",
};

function apiRepo(ref: ProviderRouteRef = github) {
  return {
    provider: ref.provider === "gh" ? "github" : ref.provider,
    platform_host: ref.platformHost ?? "github.com",
    owner: ref.owner,
    name: ref.name,
    repo_path: ref.repoPath.replace(/^\/+|\/+$/g, "").replace(/\/{2,}/g, "/"),
  };
}

function catalog(ref: ProviderRouteRef = github): WorkflowCatalog {
  return {
    repo: apiRepo(ref),
    environments: [{ name: "production" }],
    workflows: [
      {
        available: true,
        definition_sha: "definition-a",
        id: "deploy.yml",
        inputs: [],
        name: "Deploy",
        path: ".github/workflows/deploy.yml",
        state: "active",
        web_url: "https://example.test/workflows/deploy.yml",
      },
    ],
  };
}

function run(overrides: Partial<WorkflowRun> = {}): WorkflowRun {
  return {
    actor: "octocat",
    conclusion: "",
    created_at: "1970-01-01T00:00:00.000Z",
    event: "workflow_dispatch",
    head_sha: "head-a",
    id: "run-1",
    name: "Deploy",
    ref: "main",
    run_number: 12,
    status: "in_progress",
    web_url: "https://example.test/runs/1",
    workflow_id: "deploy.yml",
    ...overrides,
  };
}

interface MockRequestOptions {
  readonly params?: { readonly path?: unknown } | undefined;
  readonly signal?: AbortSignal | undefined;
  readonly body?: unknown;
}

interface ApiProbeOptions {
  readonly catalog?: (call: number, options: MockRequestOptions) => WorkflowCatalog | Promise<WorkflowCatalog>;
  readonly runs?: (call: number, options: MockRequestOptions) => WorkflowRuns | Promise<WorkflowRuns>;
  readonly jobs?: (call: number, options: MockRequestOptions) => WorkflowJobs | Promise<WorkflowJobs>;
  readonly dispatch?: (
    call: number,
    options: MockRequestOptions,
  ) => WorkflowDispatchResponse | Promise<WorkflowDispatchResponse> | { readonly error: unknown; readonly response: Response };
}

interface ApiProbe {
  readonly calls: { catalog: number; runs: number; jobs: number; dispatch: number };
  readonly observed: Array<{
    readonly method: "GET" | "POST";
    readonly path: string;
    readonly options: MockRequestOptions;
  }>;
  readonly client: GeneratedClient;
}

function makeApiProbe(options: ApiProbeOptions = {}): ApiProbe {
  const calls = { catalog: 0, runs: 0, jobs: 0, dispatch: 0 };
  const observed: ApiProbe["observed"] = [];
  const get = vi.fn(async (path: string, requestOptions: MockRequestOptions) => {
    observed.push({ method: "GET", path, options: requestOptions });
    if (path.endsWith("/workflows")) {
      calls.catalog += 1;
      const data = await (options.catalog?.(calls.catalog, requestOptions) ?? catalog());
      return { data, response: new Response(null, { status: 200 }) };
    }
    if (path.endsWith("/jobs")) {
      calls.jobs += 1;
      const data = await (options.jobs?.(calls.jobs, requestOptions) ?? { repo: apiRepo(), items: [] });
      return { data, response: new Response(null, { status: 200 }) };
    }
    calls.runs += 1;
    const data = await (options.runs?.(calls.runs, requestOptions) ?? {
      repo: apiRepo(),
      items: [run({ status: "completed", conclusion: "success" })],
      exhausted: true,
    });
    return { data, response: new Response(null, { status: 200 }) };
  });
  const post = vi.fn(async (path: string, requestOptions: MockRequestOptions) => {
    observed.push({ method: "POST", path, options: requestOptions });
    calls.dispatch += 1;
    const result = await (options.dispatch?.(calls.dispatch, requestOptions) ?? {
      accepted: true,
      locating_run: false,
      run: run(),
    });
    if ("error" in result) return result;
    return { data: result, response: new Response(null, { status: 202 }) };
  });
  const client = {
    GET: get,
    POST: post,
    PUT: vi.fn(),
    DELETE: vi.fn(),
  } as unknown as GeneratedClient;
  return { calls, observed, client };
}

function withWorkflow<A, E>(probe: ApiProbe, program: Effect.Effect<A, E, WorkflowActionsWorkflow>) {
  const layer = WorkflowActionsWorkflowLive.pipe(Layer.provide(makeGeneratedApiLayer(probe.client)));
  return program.pipe(Effect.provide(layer));
}

const settle = Effect.repeat(Effect.yieldNow, { times: 8 });

function dispatchInput(ref: ProviderRouteRef = github) {
  return {
    ref,
    workflowId: "deploy.yml",
    expectedDefinitionSha: "definition-a",
    dispatchRef: "main",
    inputs: {},
    actor: "octocat",
  } as const;
}

it.effect("keys shared reads by canonical provider, resolved host, owner, name, and normalized repo path", () => {
  const probe = makeApiProbe();
  const alternatePath = { ...github, repoPath: "octo/another-checkout" };
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      assert.strictEqual(workflowRepositoryKey(github), "github\u0000github.com\u0000octo\u0000repo\u0000octo/repo");
      const first = yield* workflow.watchRepository("surface-a", github, () => {}).pipe(Effect.forkChild);
      const second = yield* workflow.watchRepository("surface-b", alternatePath, () => {}).pipe(Effect.forkChild);
      yield* settle;

      assert.strictEqual(probe.calls.catalog, 2);
      assert.strictEqual(probe.calls.runs, 2);
      const paths = probe.observed.map((entry) => entry.path);
      assert.strictEqual(paths.filter((path) => path.endsWith("/workflows")).length, 2);
      assert.strictEqual(paths.filter((path) => path.endsWith("/runs")).length, 2);
      const firstOptions = probe.observed[0]?.options;
      assert.deepStrictEqual(firstOptions?.params?.path, { provider: "github", owner: "octo", name: "repo" });
      yield* Fiber.interrupt(second);
    }),
  );
});

it.effect("moves one owner's latest demand and shares a repository poll across other owners", () => {
  const probe = makeApiProbe();
  const other = { ...github, owner: "acme", name: "other", repoPath: "acme/other" };
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const first = yield* workflow.watchRepository("surface-a", github, () => {}).pipe(Effect.forkChild);
      const shared = yield* workflow.watchRepository("surface-b", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.runs, 1);

      const replacement = yield* workflow.watchRepository("surface-a", other, () => {}).pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.runs, 2);

      yield* Fiber.interrupt(first);
      yield* TestClock.adjust("30 seconds");
      assert.strictEqual(probe.calls.runs, 4);
      yield* Fiber.interrupt(shared);
      yield* Fiber.interrupt(replacement);
    }),
  );
});

it.effect("polls non-terminal runs after 5 seconds and terminal-only runs after 30 seconds", () => {
  const probe = makeApiProbe({
    runs: (call) => ({
      repo: apiRepo(),
      items: [run(call === 1 ? {} : { status: "completed", conclusion: "success" })],
      exhausted: true,
    }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.runs, 1);

      yield* TestClock.adjust("4999 millis");
      assert.strictEqual(probe.calls.runs, 1);
      yield* TestClock.adjust("1 millis");
      yield* settle;
      assert.strictEqual(probe.calls.runs, 2);

      yield* TestClock.adjust("29999 millis");
      assert.strictEqual(probe.calls.runs, 2);
      yield* TestClock.adjust("1 millis");
      yield* settle;
      assert.strictEqual(probe.calls.runs, 3);
      yield* Fiber.interrupt(owner);
    }),
  );
});

it.effect("stops idle polling after the final repository owner releases", () => {
  const probe = makeApiProbe();
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      yield* Fiber.interrupt(owner);
      yield* TestClock.adjust("2 minutes");
      assert.strictEqual(probe.calls.runs, 1);
      assert.strictEqual(probe.calls.catalog, 1);
    }),
  );
});

it.effect("retains an accepted dispatch after its surface releases and reconciles it to terminal", () => {
  const probe = makeApiProbe({
    dispatch: () => ({ accepted: true, locating_run: false, run: run() }),
    runs: (call) => ({
      repo: apiRepo(),
      items: [run(call === 1 ? {} : { status: "completed", conclusion: "success" })],
      exhausted: true,
    }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      const request = yield* workflow.dispatch(dispatchInput());
      yield* settle;
      yield* Fiber.interrupt(owner);

      let state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "succeeded");
      assert.strictEqual(state?.kind === "succeeded" ? state.run?.status : undefined, "in_progress");

      yield* TestClock.adjust("5 seconds");
      yield* settle;
      state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "succeeded");
      assert.strictEqual(state?.kind === "succeeded" ? state.run?.conclusion : undefined, "success");
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});

it.effect("bounds locating an accepted response without a run ID at 60 seconds", () => {
  const probe = makeApiProbe({
    dispatch: () => ({ accepted: true, locating_run: true }),
    runs: () => ({ repo: apiRepo(), items: [], exhausted: true }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const request = yield* workflow.dispatch(dispatchInput());
      yield* settle;
      let state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "locating");

      yield* TestClock.adjust("60 seconds");
      yield* settle;
      state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "locating_timed_out");
      const readsAtDeadline = probe.calls.runs;
      yield* TestClock.adjust("30 seconds");
      assert.strictEqual(probe.calls.runs, readsAtDeadline);
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});


it.effect("publishes a definite dispatch rejection as failed without replaying POST", () => {
  const problem = {
    code: "validationError",
    detail: "The workflow ref is invalid.",
    title: "Invalid workflow dispatch",
    type: "about:blank",
  };
  const probe = makeApiProbe({
    dispatch: () => ({ error: problem, response: new Response(null, { status: 400 }) }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const request = yield* workflow.dispatch(dispatchInput());
      yield* settle;
      const state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "failed");
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});

it.effect("treats a dispatch transport failure as uncertain without replaying POST", () => {
  const probe = makeApiProbe({
    dispatch: () => Promise.reject<WorkflowDispatchResponse>(new Error("connection reset after write")),
    runs: () => ({ repo: apiRepo(), items: [], exhausted: true }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const request = yield* workflow.dispatch(dispatchInput());
      yield* settle;
      const state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "uncertain");
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});
it.effect("publishes locating_timed_out when reconciliation reads keep failing", () => {
  const probe = makeApiProbe({
    dispatch: () => ({ accepted: true, locating_run: true }),
    runs: () => Promise.reject<WorkflowRuns>(new Error("provider unavailable")),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const request = yield* workflow.dispatch(dispatchInput());
      yield* settle;

      yield* TestClock.adjust("60 seconds");
      yield* settle;
      const state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "locating_timed_out");
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});

it.effect("publishes bounded ambiguous candidates for an uncertain mutation without replaying POST", () => {
  const problem = {
    code: "mutationOutcomeUnknown",
    detail: "The provider may have accepted the dispatch.",
    title: "Outcome unknown",
    type: "about:blank",
  };
  const probe = makeApiProbe({
    dispatch: () => ({ error: problem, response: new Response(null, { status: 503 }) }),
    runs: () => ({
      repo: apiRepo(),
      exhausted: true,
      items: [
        run({ id: "candidate-a" }),
        run({ id: "candidate-b", head_sha: "head-b" }),
        run({ id: "wrong-ref", ref: "release" }),
        run({ id: "wrong-actor", actor: "someone-else" }),
        run({ id: "wrong-event", event: "push" }),
        run({ id: "wrong-workflow", workflow_id: "other.yml" }),
        run({ id: "too-late", created_at: "1970-01-01T00:01:01.000Z" }),
      ],
    }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const request = yield* workflow.dispatch(dispatchInput());
      yield* settle;
      const state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "uncertain");
      assert.deepStrictEqual(
        state?.kind === "uncertain" ? state.candidates.map((candidate) => candidate.id) : [],
        ["candidate-a", "candidate-b"],
      );

      yield* TestClock.adjust("60 seconds");
      const readsAtDeadline = probe.calls.runs;
      yield* TestClock.adjust("30 seconds");
      assert.strictEqual(probe.calls.runs, readsAtDeadline);
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});

it.effect("setEnabled(false) releases reads but lets an admitted POST complete exactly once", () => {
  const response = Promise.withResolvers<WorkflowDispatchResponse>();
  const probe = makeApiProbe({ dispatch: () => response.promise });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      const request = yield* workflow.dispatch(dispatchInput());
      yield* settle;
      assert.strictEqual(probe.calls.dispatch, 1);
      const pending = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(pending?.kind, "pending");

      yield* workflow.setEnabled(false);
      yield* TestClock.adjust("2 minutes");
      assert.strictEqual(probe.calls.runs, 1);
      response.resolve({ accepted: true, locating_run: false, run: run({ status: "completed", conclusion: "success" }) });
      yield* settle;

      const state = (yield* workflow.snapshot(github)).dispatches.find((item) => item.request.id === request.id);
      assert.strictEqual(state?.kind, "succeeded");
      yield* Fiber.interrupt(owner);
    }),
  );
});

it.effect("loads jobs only for expanded consumers and aborts only after the final consumer collapses", () => {
  let aborted = false;
  const probe = makeApiProbe({
    jobs: (_call, requestOptions) => {
      const response = Promise.withResolvers<WorkflowJobs>();
      const signal = requestOptions.signal;
      if (signal === undefined) throw new Error("missing generated request abort signal");
      signal.addEventListener("abort", () => {
        aborted = true;
        response.reject(signal.reason);
      });
      return response.promise;
    },
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const repoOwner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.jobs, 0);

      const first = yield* workflow.watchJobs("row-a", github, "run-1").pipe(Effect.forkChild);
      const second = yield* workflow.watchJobs("row-b", github, "run-1").pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.jobs, 1);

      yield* Fiber.interrupt(first);
      yield* settle;
      assert.isFalse(aborted);
      yield* Fiber.interrupt(second);
      yield* settle;
      assert.isTrue(aborted);
      yield* Fiber.interrupt(repoOwner);
    }),
  );
});
