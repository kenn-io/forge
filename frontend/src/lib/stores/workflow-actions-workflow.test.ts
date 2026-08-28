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
  readonly params?:
    | {
        readonly path?: unknown;
        readonly query?: Readonly<Record<string, unknown>>;
      }
    | undefined;
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
  ) =>
    | WorkflowDispatchResponse
    | Promise<WorkflowDispatchResponse>
    | { readonly error: unknown; readonly response: Response };
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
      yield* workflow.selectWorkflow(github, "deploy.yml");
      yield* workflow.selectWorkflow(alternatePath, "deploy.yml");
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

it.effect("reads and polls runs only for the currently selected workflow", () => {
  const release = catalog();
  release.workflows = [
    ...(release.workflows ?? []),
    {
      available: true,
      definition_sha: "definition-b",
      id: "maintenance.yml",
      inputs: [],
      name: "Maintenance",
      path: ".github/workflows/maintenance.yml",
      state: "active",
      web_url: "https://example.test/workflows/maintenance.yml",
    },
  ];
  const probe = makeApiProbe({
    catalog: () => release,
    runs: (_call, options) => ({
      repo: apiRepo(),
      items: [
        run({
          id: `run-${String(options.params?.query?.workflow_id)}`,
          workflow_id: String(options.params?.query?.workflow_id),
        }),
      ],
      exhausted: true,
    }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.catalog, 1);
      assert.strictEqual(probe.calls.runs, 0);

      yield* workflow.selectWorkflow(github, "deploy.yml");
      yield* settle;
      assert.strictEqual(probe.calls.runs, 1);
      let runReads = probe.observed.filter((entry) => entry.path.endsWith("/runs"));
      assert.deepStrictEqual(
        runReads.map((entry) => entry.options.params?.query),
        [{ workflow_id: "deploy.yml", per_page: 50 }],
      );

      yield* workflow.selectWorkflow(github, "maintenance.yml");
      yield* settle;
      assert.strictEqual(probe.calls.runs, 2);
      let snapshot = yield* workflow.snapshot(github);
      assert.deepStrictEqual(
        snapshot.runs.map((item) => item.workflow_id),
        ["maintenance.yml"],
      );

      yield* TestClock.adjust("5 seconds");
      yield* settle;
      assert.strictEqual(probe.calls.runs, 3);
      runReads = probe.observed.filter((entry) => entry.path.endsWith("/runs"));
      assert.deepStrictEqual(
        runReads.map((entry) => entry.options.params?.query),
        [
          { workflow_id: "deploy.yml", per_page: 50 },
          { workflow_id: "maintenance.yml", per_page: 50 },
          { workflow_id: "maintenance.yml", per_page: 50 },
        ],
      );
      assert.isTrue(runReads.every((entry) => entry.options.params?.query?.workflow_id !== ""));
      snapshot = yield* workflow.snapshot(github);
      assert.strictEqual(snapshot.selectedWorkflow?.id, "maintenance.yml");
      yield* Fiber.interrupt(owner);
    }),
  );
});

it.effect("retries a failed visible catalog read only after the idle cadence", () => {
  const probe = makeApiProbe({
    catalog: (call) => (call === 1 ? Promise.reject<WorkflowCatalog>(new Error("provider unavailable")) : catalog()),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.catalog, 1);
      assert.strictEqual(probe.calls.runs, 0);

      yield* TestClock.adjust("29999 millis");
      yield* settle;
      assert.strictEqual(probe.calls.catalog, 1);

      yield* TestClock.adjust("1 millis");
      yield* settle;
      assert.strictEqual(probe.calls.catalog, 2);
      assert.strictEqual((yield* workflow.snapshot(github)).catalog?.workflows?.[0]?.id, "deploy.yml");
      yield* Fiber.interrupt(owner);
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
      yield* workflow.selectWorkflow(github, "deploy.yml");
      yield* workflow.selectWorkflow(other, "deploy.yml");
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
      yield* workflow.selectWorkflow(github, "deploy.yml");
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

it.effect("wakes an idle selected-workflow poll when dispatch is accepted", () => {
  const probe = makeApiProbe({
    dispatch: () => ({
      accepted: true,
      locating_run: false,
      run: run({ id: "run-2" }),
    }),
    runs: (call) => ({
      repo: apiRepo(),
      items: [run(call === 1 ? { status: "completed", conclusion: "success" } : { id: "run-2" })],
      exhausted: true,
    }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.selectWorkflow(github, "deploy.yml");
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.runs, 1);

      yield* workflow.dispatch(dispatchInput());
      yield* settle;
      assert.strictEqual(probe.calls.runs, 2);
      assert.deepStrictEqual(
        (yield* workflow.snapshot(github)).runs.map((item) => item.id),
        ["run-2"],
      );
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
      yield* workflow.selectWorkflow(github, "deploy.yml");
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
      assert.deepStrictEqual(state?.kind === "uncertain" ? state.candidates.map((candidate) => candidate.id) : [], [
        "candidate-a",
        "candidate-b",
      ]);

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
      yield* workflow.selectWorkflow(github, "deploy.yml");
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
      response.resolve({
        accepted: true,
        locating_run: false,
        run: run({ status: "completed", conclusion: "success" }),
      });
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

it.effect("cannot interrupt the admitted dispatch handoff between pending publication and POST release", () => {
  const probe = makeApiProbe();
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      let interruptDispatch: (() => void) | undefined;
      const owner = yield* workflow
        .watchRepository("handoff-observer", github, (snapshot) => {
          if (snapshot.dispatches.some((state) => state.kind === "pending")) interruptDispatch?.();
        })
        .pipe(Effect.forkChild);
      yield* settle;

      const dispatch = yield* workflow.dispatch(dispatchInput()).pipe(Effect.forkChild);
      interruptDispatch = () => dispatch.interruptUnsafe();
      yield* settle;

      assert.strictEqual(probe.calls.dispatch, 1);
      assert.strictEqual((yield* workflow.snapshot(github)).dispatches.at(-1)?.kind, "succeeded");
      yield* Fiber.interrupt(owner);
    }),
  );
});

it.effect("releases a repository owner interrupted by its initial observer", () => {
  const probe = makeApiProbe();
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      let interruptOwner: (() => void) | undefined;
      const interrupted = yield* workflow
        .watchRepository("interrupted-owner", github, () => interruptOwner?.())
        .pipe(Effect.forkChild);
      interruptOwner = () => interrupted.interruptUnsafe();
      yield* settle;

      const survivor = yield* workflow.watchRepository("survivor", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      yield* Fiber.interrupt(survivor);
      const readsAfterRelease = probe.calls.runs;
      yield* TestClock.adjust("2 minutes");

      assert.strictEqual(probe.calls.runs, readsAfterRelease);
    }),
  );
});

it.effect("releases a jobs owner interrupted while replacing its previous run", () => {
  const oldRead = Promise.withResolvers<WorkflowJobs>();
  let interruptReplacement: (() => void) | undefined;
  const probe = makeApiProbe({
    jobs: (call, requestOptions) => {
      if (call > 1) return { repo: apiRepo(), items: [] };
      const signal = requestOptions.signal;
      if (signal === undefined) throw new Error("missing generated request abort signal");
      signal.addEventListener("abort", () => {
        interruptReplacement?.();
        oldRead.reject(signal.reason);
      });
      return oldRead.promise;
    },
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const previous = yield* workflow.watchJobs("row", github, "run-old").pipe(Effect.forkChild);
      yield* settle;
      const replacement = yield* workflow.watchJobs("row", github, "run-new").pipe(Effect.forkChild);
      interruptReplacement = () => replacement.interruptUnsafe();
      yield* settle;

      const helper = yield* workflow.watchJobs("helper", github, "run-new").pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.jobs, 2);
      yield* Fiber.interrupt(previous);
      yield* Fiber.interrupt(helper);
    }),
  );
});

it.effect("restarts a repository loop when demand arrives as the prior loop exits", () => {
  const probe = makeApiProbe({
    dispatch: () => ({ accepted: true, locating_run: true }),
    runs: () => ({ repo: apiRepo(), items: [], exhausted: true }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.selectWorkflow(github, "deploy.yml");
      yield* workflow.dispatch(dispatchInput());
      yield* settle;

      yield* TestClock.adjust("60 seconds");
      const owner = yield* workflow.watchRepository("deadline-owner", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      const readsAfterClaim = probe.calls.runs;
      yield* TestClock.adjust("30 seconds");
      yield* settle;

      assert.isAbove(probe.calls.runs, readsAfterClaim);
      yield* Fiber.interrupt(owner);
    }),
  );
});

it.effect("clears interrupted catalog, runs, and jobs loading markers when disabled", () => {
  const catalogRead = Promise.withResolvers<WorkflowCatalog>();
  const runsRead = Promise.withResolvers<WorkflowRuns>();
  const jobsRead = Promise.withResolvers<WorkflowJobs>();
  const probe = makeApiProbe({
    catalog: () => catalogRead.promise,
    runs: () => runsRead.promise,
    jobs: () => jobsRead.promise,
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.selectWorkflow(github, "deploy.yml");
      const repositoryOwner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      const jobsOwner = yield* workflow.watchJobs("row", github, "run-1").pipe(Effect.forkChild);
      yield* settle;
      catalogRead.resolve(catalog());
      yield* settle;
      assert.deepStrictEqual((yield* workflow.snapshot(github)).loading, {
        catalog: false,
        runs: true,
        jobs: ["run-1"],
      });

      yield* workflow.setEnabled(false);
      assert.deepStrictEqual((yield* workflow.snapshot(github)).loading, {
        catalog: false,
        runs: false,
        jobs: [],
      });
      yield* Fiber.interrupt(repositoryOwner);
      yield* Fiber.interrupt(jobsOwner);
    }),
  );
});

it.effect("starts one replacement jobs read for a consumer that joined the failing in-flight read", () => {
  const firstRead = Promise.withResolvers<WorkflowJobs>();
  const probe = makeApiProbe({
    jobs: (call) =>
      call === 1
        ? firstRead.promise
        : {
            repo: apiRepo(),
            items: [
              {
                id: "job-recovered",
                name: "recovered",
                status: "completed",
                conclusion: "success",
                steps: [],
              },
            ],
          },
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const first = yield* workflow.watchJobs("row-a", github, "run-1").pipe(Effect.forkChild);
      yield* settle;
      const joined = yield* workflow.watchJobs("row-b", github, "run-1").pipe(Effect.forkChild);
      yield* settle;
      firstRead.reject(new Error("first read failed"));
      yield* settle;

      assert.strictEqual(probe.calls.jobs, 2);
      assert.deepStrictEqual(
        (yield* workflow.snapshot(github)).jobs["run-1"]?.map((job) => job.id),
        ["job-recovered"],
      );
      yield* TestClock.adjust("1 minute");
      assert.strictEqual(probe.calls.jobs, 2);
      yield* Fiber.interrupt(first);
      yield* Fiber.interrupt(joined);
    }),
  );
});

it.effect(
  "refreshes a cached catalog after a definition conflict and starts a fresh cycle without replaying POST",
  () => {
    const refreshed = catalog();
    refreshed.workflows = [
      {
        ...refreshed.workflows[0]!,
        definition_sha: "definition-b",
        inputs: [
          {
            name: "channel",
            type: "choice",
            required: true,
            has_default: true,
            default: "stable",
            options: ["stable", "beta"],
          },
        ],
      },
    ];
    const conflict = {
      code: "conflict",
      detail: "Workflow definition changed.",
      details: { reason: "workflow_definition_changed" },
      status: 409,
      title: "Conflict",
      type: "about:blank",
    };
    const probe = makeApiProbe({
      catalog: (call) => (call === 1 ? catalog() : refreshed),
      dispatch: () => ({ error: conflict, response: new Response(null, { status: 409 }) }),
    });
    return withWorkflow(
      probe,
      Effect.gen(function* () {
        const workflow = yield* WorkflowActionsWorkflow;
        yield* workflow.selectWorkflow(github, "deploy.yml");
        const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
        yield* settle;
        yield* workflow.dispatch(dispatchInput());
        yield* settle;
        assert.strictEqual((yield* workflow.snapshot(github)).dispatches.at(-1)?.kind, "failed");

        yield* workflow.refreshCatalog(github, "deploy.yml");
        const snapshot = yield* workflow.snapshot(github);
        assert.strictEqual(probe.calls.catalog, 2);
        assert.strictEqual(probe.calls.dispatch, 1);
        assert.strictEqual(snapshot.selectedWorkflow?.definition_sha, "definition-b");
        assert.deepStrictEqual(
          snapshot.selectedWorkflow?.inputs?.map((input) => input.name),
          ["channel"],
        );
        assert.isFalse(snapshot.dispatches.some((state) => state.request.workflowId === "deploy.yml"));
        yield* Fiber.interrupt(owner);
      }),
    );
  },
);

it.effect("retains a conflict reload failure across successful run polling until it is cleared", () => {
  const conflict = {
    code: "conflict",
    detail: "Workflow definition changed.",
    details: { reason: "workflow_definition_changed" },
    status: 409,
    title: "Conflict",
    type: "about:blank",
  };
  const probe = makeApiProbe({
    catalog: (call) => (call === 1 ? catalog() : Promise.reject<WorkflowCatalog>(new Error("catalog unavailable"))),
    dispatch: () => ({ error: conflict, response: new Response(null, { status: 409 }) }),
    runs: () => ({
      repo: apiRepo(),
      items: [run({ status: "completed", conclusion: "success" })],
      exhausted: true,
    }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.selectWorkflow(github, "deploy.yml");
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      yield* workflow.dispatch(dispatchInput());
      yield* settle;

      yield* workflow.refreshCatalog(github, "deploy.yml");
      let snapshot = yield* workflow.snapshot(github);
      assert.strictEqual(snapshot.catalogRefreshErrors["deploy.yml"]?._tag, "TransientTransportError");
      assert.strictEqual(probe.calls.dispatch, 1);

      yield* TestClock.adjust("30 seconds");
      yield* settle;
      snapshot = yield* workflow.snapshot(github);
      assert.strictEqual(snapshot.error, null);
      assert.strictEqual(snapshot.catalogRefreshErrors["deploy.yml"]?._tag, "TransientTransportError");
      assert.strictEqual(snapshot.dispatches.at(-1)?.kind, "failed");

      yield* workflow.clearCatalogRefreshError(github, "deploy.yml");
      snapshot = yield* workflow.snapshot(github);
      assert.strictEqual(snapshot.catalogRefreshErrors["deploy.yml"], undefined);
      assert.strictEqual(snapshot.dispatches.at(-1)?.kind, "failed");
      assert.strictEqual(probe.calls.dispatch, 1);
      yield* Fiber.interrupt(owner);
    }),
  );
});

it.effect("ignores an older reload failure after a newer reload succeeds", () => {
  const olderCatalog = Promise.withResolvers<WorkflowCatalog>();
  const refreshed = catalog();
  refreshed.workflows = [{ ...refreshed.workflows[0]!, definition_sha: "definition-newest" }];
  const conflict = {
    code: "conflict",
    detail: "Workflow definition changed.",
    details: { reason: "workflow_definition_changed" },
    status: 409,
    title: "Conflict",
    type: "about:blank",
  };
  const probe = makeApiProbe({
    catalog: (call) => (call === 1 ? catalog() : call === 2 ? olderCatalog.promise : refreshed),
    dispatch: () => ({ error: conflict, response: new Response(null, { status: 409 }) }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      yield* workflow.dispatch(dispatchInput());
      yield* settle;

      const older = yield* workflow.refreshCatalog(github, "deploy.yml").pipe(Effect.forkChild);
      yield* settle;
      assert.strictEqual(probe.calls.catalog, 2);
      yield* workflow.refreshCatalog(github, "deploy.yml");
      olderCatalog.reject(new Error("older catalog failed"));
      yield* settle;
      yield* Fiber.await(older);

      const snapshot = yield* workflow.snapshot(github);
      assert.strictEqual(snapshot.catalog?.workflows?.[0]?.definition_sha, "definition-newest");
      assert.strictEqual(snapshot.catalogRefreshErrors["deploy.yml"], undefined);
      assert.isFalse(snapshot.dispatches.some((state) => state.request.workflowId === "deploy.yml"));
      assert.strictEqual(probe.calls.dispatch, 1);
      yield* Fiber.interrupt(owner);
    }),
  );
});

it.effect("keeps the newest reload failure when an older reload succeeds later", () => {
  const olderCatalog = Promise.withResolvers<WorkflowCatalog>();
  const olderSuccess = catalog();
  olderSuccess.workflows = [{ ...olderSuccess.workflows[0]!, definition_sha: "definition-stale" }];
  const conflict = {
    code: "conflict",
    detail: "Workflow definition changed.",
    details: { reason: "workflow_definition_changed" },
    status: 409,
    title: "Conflict",
    type: "about:blank",
  };
  const probe = makeApiProbe({
    catalog: (call) =>
      call === 1
        ? catalog()
        : call === 2
          ? olderCatalog.promise
          : Promise.reject<WorkflowCatalog>(new Error("newest catalog failed")),
    dispatch: () => ({ error: conflict, response: new Response(null, { status: 409 }) }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      yield* workflow.dispatch(dispatchInput());
      yield* settle;

      const older = yield* workflow.refreshCatalog(github, "deploy.yml").pipe(Effect.forkChild);
      yield* settle;
      yield* workflow.refreshCatalog(github, "deploy.yml");
      let snapshot = yield* workflow.snapshot(github);
      assert.strictEqual(snapshot.catalogRefreshErrors["deploy.yml"]?._tag, "TransientTransportError");

      olderCatalog.resolve(olderSuccess);
      yield* settle;
      yield* Fiber.await(older);
      snapshot = yield* workflow.snapshot(github);
      assert.strictEqual(snapshot.catalog?.workflows?.[0]?.definition_sha, "definition-a");
      assert.strictEqual(snapshot.catalogRefreshErrors["deploy.yml"]?._tag, "TransientTransportError");
      assert.strictEqual(snapshot.dispatches.at(-1)?.kind, "failed");
      assert.strictEqual(probe.calls.dispatch, 1);
      yield* Fiber.interrupt(owner);
    }),
  );
});

it.effect("requires an explicit new cycle before deliberately dispatching the same workflow twice", () => {
  const probe = makeApiProbe({
    dispatch: () => ({
      accepted: true,
      locating_run: false,
      actor: "octocat",
      run: run({ status: "completed", conclusion: "success" }),
    }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.dispatch(dispatchInput());
      yield* settle;
      assert.strictEqual(probe.calls.dispatch, 1);
      assert.strictEqual((yield* workflow.snapshot(github)).dispatches.at(-1)?.kind, "succeeded");

      yield* workflow.newDispatchCycle(github, "deploy.yml");
      assert.strictEqual(probe.calls.dispatch, 1);
      assert.isFalse(
        (yield* workflow.snapshot(github)).dispatches.some((state) => state.request.workflowId === "deploy.yml"),
      );

      yield* workflow.dispatch(dispatchInput());
      yield* settle;
      assert.strictEqual(probe.calls.dispatch, 2);
      assert.strictEqual((yield* workflow.snapshot(github)).dispatches.at(-1)?.kind, "succeeded");
    }),
  );
});

it.effect("starts each queued dispatch reconciliation window immediately before its own POST", () => {
  const firstResponse = Promise.withResolvers<WorkflowDispatchResponse>();
  const probe = makeApiProbe({
    dispatch: (call) => (call === 1 ? firstResponse.promise : { accepted: true, locating_run: true, actor: "octocat" }),
    runs: () => ({ repo: apiRepo(), items: [], exhausted: true }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      const first = yield* workflow.dispatch(dispatchInput());
      const second = yield* workflow.dispatch(dispatchInput());
      yield* settle;
      assert.strictEqual(probe.calls.dispatch, 1);

      yield* TestClock.adjust("61 seconds");
      firstResponse.resolve({
        accepted: true,
        locating_run: false,
        actor: "octocat",
        run: run({ status: "completed", conclusion: "success" }),
      });
      yield* settle;
      assert.strictEqual(probe.calls.dispatch, 2);
      assert.strictEqual(
        (yield* workflow.snapshot(github)).dispatches.find((state) => state.request.id === second.id)?.kind,
        "locating",
      );
      assert.strictEqual(
        (yield* workflow.snapshot(github)).dispatches.find((state) => state.request.id === first.id)?.kind,
        "succeeded",
      );

      yield* TestClock.adjust("59 seconds");
      yield* settle;
      assert.strictEqual(
        (yield* workflow.snapshot(github)).dispatches.find((state) => state.request.id === second.id)?.kind,
        "locating",
      );
      yield* TestClock.adjust("1 second");
      yield* settle;
      assert.strictEqual(
        (yield* workflow.snapshot(github)).dispatches.find((state) => state.request.id === second.id)?.kind,
        "locating_timed_out",
      );
      assert.strictEqual(probe.calls.dispatch, 2);
    }),
  );
});

it.effect("serializes dispatches per repository while different repositories post concurrently", () => {
  const firstResponse = Promise.withResolvers<WorkflowDispatchResponse>();
  const probe = makeApiProbe({
    dispatch: (call) =>
      call === 1
        ? firstResponse.promise
        : {
            accepted: true,
            locating_run: false,
            actor: "octocat",
            run: run({ id: "other-run", status: "completed", conclusion: "success" }),
          },
  });
  const other = { ...github, name: "other", repoPath: "octo/other" };
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.dispatch(dispatchInput());
      yield* workflow.dispatch(dispatchInput(other));
      yield* settle;
      assert.strictEqual(probe.calls.dispatch, 2);
      assert.strictEqual((yield* workflow.snapshot(other)).dispatches.at(-1)?.kind, "succeeded");

      firstResponse.resolve({
        accepted: true,
        locating_run: false,
        actor: "octocat",
        run: run({ status: "completed", conclusion: "success" }),
      });
      yield* settle;
      assert.strictEqual((yield* workflow.snapshot(github)).dispatches.at(-1)?.kind, "succeeded");
    }),
  );
});

it.effect("never reconciles another maintainer's run when the dispatch actor is unknown", () => {
  const problem = {
    code: "mutationOutcomeUnknown",
    detail: "The provider may have accepted the dispatch.",
    status: 502,
    title: "Outcome unknown",
    type: "about:blank",
  };
  const probe = makeApiProbe({
    dispatch: () => ({ error: problem, response: new Response(null, { status: 502 }) }),
    runs: () => ({ repo: apiRepo(), exhausted: true, items: [run({ actor: "another-maintainer" })] }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.dispatch({ ...dispatchInput(), actor: undefined });
      yield* settle;
      const state = (yield* workflow.snapshot(github)).dispatches.at(-1);
      assert.strictEqual(state?.kind, "uncertain");
      assert.deepStrictEqual(state?.kind === "uncertain" ? state.candidates : [], []);
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});

it.effect("uses an accepted response actor to reconcile a locating run", () => {
  const probe = makeApiProbe({
    dispatch: () => ({ accepted: true, locating_run: true, actor: "octocat" }),
    runs: () => ({ repo: apiRepo(), exhausted: true, items: [run({ actor: "octocat" })] }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.dispatch({ ...dispatchInput(), actor: undefined });
      yield* settle;
      const state = (yield* workflow.snapshot(github)).dispatches.at(-1);
      assert.strictEqual(state?.kind, "succeeded");
      assert.strictEqual(state?.request.actor, "octocat");
      assert.strictEqual(state?.kind === "succeeded" ? state.run?.id : undefined, "run-1");
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});

it.effect("keeps a locating dispatch fenced when two matching provider runs are ambiguous", () => {
  const probe = makeApiProbe({
    dispatch: () => ({ accepted: true, locating_run: true, actor: "octocat" }),
    runs: () => ({
      repo: apiRepo(),
      exhausted: true,
      items: [
        run({ id: "matching-run-a", actor: "octocat" }),
        run({ id: "matching-run-b", actor: "octocat", head_sha: "head-b" }),
      ],
    }),
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.dispatch({ ...dispatchInput(), actor: undefined });
      yield* settle;
      const state = (yield* workflow.snapshot(github)).dispatches.at(-1);
      assert.strictEqual(state?.kind, "locating");
      assert.strictEqual(state?.kind === "succeeded" ? state.run?.id : undefined, undefined);
      assert.strictEqual(probe.calls.dispatch, 1);
    }),
  );
});

it.effect("appends older run pages and preserves them while polling page one", () => {
  const observedCursors: unknown[] = [];
  let firstPageReads = 0;
  const probe = makeApiProbe({
    runs: (_call, options) => {
      const cursor = options.params?.query?.["cursor"];
      observedCursors.push(cursor);
      if (cursor === "cursor-2") {
        return {
          repo: apiRepo(),
          items: [run({ id: "run-older-2", run_number: 10 }), run({ id: "run-older-1", run_number: 11 })],
          exhausted: true,
        };
      }
      firstPageReads += 1;
      return {
        repo: apiRepo(),
        items: [
          run({
            id: firstPageReads === 1 ? "run-current-1" : "run-current-2",
            run_number: firstPageReads === 1 ? 12 : 13,
            status: "completed",
            conclusion: "success",
          }),
        ],
        next_cursor: "cursor-2",
        exhausted: false,
      };
    },
  });
  return withWorkflow(
    probe,
    Effect.gen(function* () {
      const workflow = yield* WorkflowActionsWorkflow;
      yield* workflow.selectWorkflow(github, "deploy.yml");
      const owner = yield* workflow.watchRepository("surface", github, () => {}).pipe(Effect.forkChild);
      yield* settle;
      let snapshot = yield* workflow.snapshot(github);
      assert.deepStrictEqual(
        snapshot.runs.map((item) => item.id),
        ["run-current-1"],
      );
      assert.strictEqual(snapshot.runsPage.nextCursor, "cursor-2");
      assert.isFalse(snapshot.runsPage.exhausted);

      yield* workflow.loadMoreRuns(github);
      snapshot = yield* workflow.snapshot(github);
      assert.deepStrictEqual(
        snapshot.runs.map((item) => item.id),
        ["run-current-1", "run-older-2", "run-older-1"],
      );
      assert.isTrue(snapshot.runsPage.exhausted);

      yield* TestClock.adjust("30 seconds");
      yield* settle;
      snapshot = yield* workflow.snapshot(github);
      assert.deepStrictEqual(
        snapshot.runs.map((item) => item.id),
        ["run-current-2", "run-older-2", "run-older-1"],
      );
      assert.deepStrictEqual(observedCursors, [undefined, "cursor-2", undefined]);
      yield* Fiber.interrupt(owner);
    }),
  );
});
