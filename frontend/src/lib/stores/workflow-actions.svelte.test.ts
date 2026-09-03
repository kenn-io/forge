import { Effect, Fiber, Layer, ManagedRuntime } from "effect";
import type { Effect as EffectType } from "effect/Effect";
import type { Exit as ExitType } from "effect/Exit";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { AppExecution, AppRuntime, AppServices, CommandRunOptions, OwnedAppRuntime } from "../app/runtime.js";
import type { components } from "../api/generated/schema.js";
import { makeGeneratedApiLayer } from "../api/generated-api.js";
import type { GeneratedClient } from "../api/generated-api.js";
import type { ProviderRouteRef } from "../api/provider-routes.js";
import { WorkflowActionsWorkflow, WorkflowActionsWorkflowLive } from "./workflow-actions-workflow.js";
import { createWorkflowActionsStore } from "./workflow-actions.svelte.js";

type WorkflowRun = components["schemas"]["WorkflowRunResponse"];

const ref: ProviderRouteRef = {
  provider: "github",
  platformHost: "github.com",
  owner: "octo",
  name: "repo",
  repoPath: "octo/repo",
};

function repo() {
  return {
    provider: "github",
    platform_host: "github.com",
    owner: "octo",
    name: "repo",
    repo_path: "octo/repo",
  };
}

function workflow() {
  return {
    available: true,
    definition_sha: "definition-a",
    id: "deploy.yml",
    inputs: [],
    name: "Deploy",
    path: ".github/workflows/deploy.yml",
    state: "active",
    web_url: "https://example.test/workflows/deploy.yml",
  };
}

function run(): WorkflowRun {
  return {
    actor: "octocat",
    conclusion: "success",
    created_at: "2026-08-28T12:00:00Z",
    event: "workflow_dispatch",
    head_sha: "head-a",
    id: "run-1",
    name: "Deploy",
    ref: "main",
    run_number: 12,
    status: "completed",
    web_url: "https://example.test/runs/1",
    workflow_id: "deploy.yml",
  };
}

function makeApiProbe() {
  const get = vi.fn(async (path: string) => {
    if (path.endsWith("/workflows")) {
      return {
        data: { repo: repo(), environments: [{ name: "production" }], workflows: [workflow()] },
        response: new Response(null, { status: 200 }),
      };
    }
    if (path.endsWith("/jobs")) {
      return {
        data: {
          repo: repo(),
          items: [
            {
              id: "job-1",
              name: "deploy",
              status: "completed",
              conclusion: "success",
              steps: [],
            },
          ],
        },
        response: new Response(null, { status: 200 }),
      };
    }
    return {
      data: { repo: repo(), exhausted: true, items: [run()] },
      response: new Response(null, { status: 200 }),
    };
  });
  const post = vi.fn(async () => ({
    data: { accepted: true, locating_run: false, run: run() },
    response: new Response(null, { status: 202 }),
  }));
  const client = { GET: get, POST: post, PUT: vi.fn(), DELETE: vi.fn() } as unknown as GeneratedClient;
  return { get, post, client };
}

function makeWorkflowRuntime(client: GeneratedClient): OwnedAppRuntime {
  const layer = WorkflowActionsWorkflowLive.pipe(Layer.provide(makeGeneratedApiLayer(client)));
  const managed = ManagedRuntime.make(layer);

  function runCommand<A, E>(
    program: EffectType<A, E, AppServices>,
    _options: CommandRunOptions<E>,
  ): AppExecution<A, E> {
    // This focused runtime intentionally supplies only the service required by the store under test.
    const executable = program as unknown as EffectType<A, E, WorkflowActionsWorkflow>;
    const fiber = managed.runFork(executable);
    const completion = Promise.withResolvers<ExitType<A, E>>();
    fiber.addObserver(completion.resolve);
    return {
      interrupt: () => fiber.interruptUnsafe(),
      await: Fiber.await(fiber),
      exit: completion.promise,
    };
  }

  const runtime: AppRuntime = {
    runCommand,
    runMicrotask: (callback, options) =>
      runCommand(Effect.sync(callback), {
        ...options,
        onFailure: () => {},
      }),
  };
  return { ...runtime, disposeEffect: managed.disposeEffect };
}

let runtime: OwnedAppRuntime | undefined;

afterEach(async () => {
  if (runtime !== undefined) await Effect.runPromise(runtime.disposeEffect);
  runtime = undefined;
});

describe("workflow Actions projection store", () => {
  it("projects catalog, environments, selection, runs, lazy jobs, loading, and dispatch states", async () => {
    const probe = makeApiProbe();
    runtime = makeWorkflowRuntime(probe.client);
    const store = createWorkflowActionsStore({ runtime });

    store.claimRepository("actions-page", ref);
    await vi.waitFor(() => expect(store.getCatalog(ref)?.workflows).toHaveLength(1));
    expect(store.getEnvironments(ref)).toEqual([{ name: "production" }]);
    store.selectWorkflow(ref, "deploy.yml");
    await vi.waitFor(() => expect(store.getSelectedWorkflow(ref)?.name).toBe("Deploy"));
    await vi.waitFor(() => expect(store.getRuns(ref).map((item) => item.id)).toEqual(["run-1"]));
    expect(store.getLoading(ref)).toEqual({ catalog: false, runs: false, jobs: [] });

    expect(probe.get.mock.calls.some(([path]) => String(path).endsWith("/jobs"))).toBe(false);
    store.expandRun("actions-page:run-1", ref, "run-1");
    await vi.waitFor(() => expect(store.getJobs(ref, "run-1").map((job) => job.id)).toEqual(["job-1"]));

    store.dispatch({
      ref,
      workflowId: "deploy.yml",
      expectedDefinitionSha: "definition-a",
      dispatchRef: "main",
      inputs: {},
      actor: "octocat",
    });
    await vi.waitFor(() => expect(store.getDispatches(ref).at(-1)?.kind).toBe("succeeded"));
    expect(probe.post).toHaveBeenCalledTimes(1);
    store.newDispatchCycle(ref, "deploy.yml");
    await vi.waitFor(() => expect(store.getDispatches(ref)).toEqual([]));
    expect(probe.post).toHaveBeenCalledTimes(1);

    store.refreshCatalog(ref, "deploy.yml");
    await vi.waitFor(() =>
      expect(probe.get.mock.calls.filter(([path]) => String(path).endsWith("/workflows"))).toHaveLength(2),
    );
    expect(probe.post).toHaveBeenCalledTimes(1);
  });

  it("releases synchronous owner launchers and disables all future reads", async () => {
    const probe = makeApiProbe();
    runtime = makeWorkflowRuntime(probe.client);
    const store = createWorkflowActionsStore({ runtime });

    store.claimRepository("actions-page", ref);
    await vi.waitFor(() => expect(store.getCatalog(ref)).not.toBeNull());
    store.expandRun("actions-page:run-1", ref, "run-1");
    await vi.waitFor(() => expect(store.getJobs(ref, "run-1")).toHaveLength(1));

    store.releaseRepository("actions-page");
    store.collapseRun("actions-page:run-1");
    store.setEnabled(false);
    expect(store.getCatalog(ref)).toBeNull();

    const reads = probe.get.mock.calls.length;
    store.claimRepository("disabled-surface", ref);
    store.expandRun("disabled-row", ref, "run-1");
    expect(probe.get).toHaveBeenCalledTimes(reads);
  });

  it("adopts app-owned accepted dispatch state in a replacement presenter", async () => {
    const probe = makeApiProbe();
    runtime = makeWorkflowRuntime(probe.client);
    const initiating = createWorkflowActionsStore({ runtime });
    initiating.claimRepository("dialog", ref);
    initiating.dispatch({
      ref,
      workflowId: "deploy.yml",
      expectedDefinitionSha: "definition-a",
      dispatchRef: "main",
      inputs: {},
      actor: "octocat",
    });
    await vi.waitFor(() => expect(initiating.getDispatches(ref).at(-1)?.kind).toBe("succeeded"));
    initiating.releaseRepository("dialog");

    const replacement = createWorkflowActionsStore({ runtime });
    replacement.claimRepository("actions-page", ref);
    await vi.waitFor(() => expect(replacement.getDispatches(ref).at(-1)?.kind).toBe("succeeded"));
    expect(probe.post).toHaveBeenCalledTimes(1);
  });
});
