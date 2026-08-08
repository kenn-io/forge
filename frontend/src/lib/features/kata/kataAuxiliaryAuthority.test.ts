import { Cause, Effect, Exit, Option } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import { createRuntimeClient } from "../../api/runtime.js";
import type { KataSnapshotIntent, KataWorkspaceSnapshotResponse } from "../../api/kata/snapshot.js";
import { TransientTransportError } from "../../api/effect-errors.js";
import type { AppServices, OwnedAppRuntime } from "../../app/runtime.js";
import { makeTestAppRuntime } from "../../testing/effect-layers.js";
import { createKataAuxiliaryAuthority } from "./kataAuxiliaryAuthority.svelte.js";
import type { KataSnapshotLoader } from "./kataWorkspaceAuthorityController.svelte.js";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function snapshot(
  intent: KataSnapshotIntent,
  overrides: Partial<KataWorkspaceSnapshotResponse> = {},
): KataWorkspaceSnapshotResponse {
  const generation = overrides.generation ?? 1;
  return {
    server_instance_id: "server-a",
    daemon_id: intent.daemon_id ?? "home",
    intent: { scope: "global", authority: "all" },
    generation,
    invalidation_epoch: overrides.invalidation_epoch ?? generation,
    event_cursor: overrides.event_cursor ?? generation,
    fetched_at: "2026-07-21T10:00:00Z",
    projects: [],
    member_issue_uids: [`issue-${generation}`],
    issues: [
      {
        id: generation,
        uid: `issue-${generation}`,
        project_id: 7,
        project_uid: "project-a",
        project_name: "Project A",
        short_id: `a${generation}`,
        qualified_id: `Project A#a${generation}`,
        title: `Authority task ${generation}`,
        body: "Task body",
        status: generation === 1 ? "open" : "closed",
        metadata: {},
        revision: generation,
        author: "alice",
        labels: [],
        blocks: null,
        blocked_by: null,
        related: null,
        created_at: "2026-07-21T09:00:00Z",
        updated_at: "2026-07-21T09:30:00Z",
      },
    ],
    enrichment: intent.selected_issue_uid
      ? {
          selected_issue_uid: intent.selected_issue_uid,
          selected_detail: {
            etag: `"rev-${generation}"`,
            workspace_target: { available: false },
            detail: {
              issue: {
                id: generation,
                uid: intent.selected_issue_uid,
                project_id: 7,
                project_uid: "project-a",
                project_name: "Project A",
                short_id: `a${generation}`,
                qualified_id: `Project A#a${generation}`,
                title: `Authority task ${generation}`,
                body: "Task body",
                status: "open",
                metadata: {},
                revision: generation,
                author: "alice",
                labels: [],
                blocks: null,
                blocked_by: null,
                related: null,
                created_at: "2026-07-21T09:00:00Z",
                updated_at: "2026-07-21T09:30:00Z",
              },
              comments: [],
              labels: [],
              links: [],
              children: [],
            },
          },
        }
      : {},
    ...overrides,
  };
}

const runtimes: OwnedAppRuntime[] = [];

function testRuntime(): OwnedAppRuntime {
  const fetchImpl: typeof fetch = (input, init) =>
    new Promise<Response>((_resolve, reject) => {
      const request = input instanceof Request ? input : new Request(input, init);
      request.signal.addEventListener("abort", () => reject(new DOMException("request aborted", "AbortError")), {
        once: true,
      });
    });
  const runtime = makeTestAppRuntime(createRuntimeClient(fetchImpl));
  runtimes.push(runtime);
  return runtime;
}

function effectLoader(
  loadSnapshot: (intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>,
): KataSnapshotLoader {
  return (intent) =>
    Effect.tryPromise({
      try: () => loadSnapshot(intent),
      catch: (cause) => TransientTransportError.make({ operation: "load test Kata snapshot", cause }),
    });
}

async function runAppEffect<A, E>(runtime: OwnedAppRuntime, program: Effect.Effect<A, E, AppServices>): Promise<A> {
  const execution = runtime.runCommand(program, {
    operation: "test Kata auxiliary authority",
    safeContext: {},
    onFailure: () => {},
  });
  const exit = await Effect.runPromise(execution.await);
  if (Exit.isSuccess(exit)) return exit.value;
  const failure = Cause.findErrorOption(exit.cause);
  if (Option.isSome(failure)) {
    if (failure.value instanceof TransientTransportError && failure.value.cause instanceof Error) {
      throw failure.value.cause;
    }
    throw failure.value;
  }
  throw new Error(Cause.pretty(exit.cause));
}

afterEach(async () => {
  await Promise.all(runtimes.splice(0).map((runtime) => Effect.runPromise(runtime.disposeEffect)));
});

describe("Kata auxiliary authority", () => {
  it("does not restart from work described before terminal stop", async () => {
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => snapshot(intent));
    const runtime = testRuntime();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot: effectLoader(loadSnapshot) });
    const describedLoad = authority.load("home");

    await runAppEffect(runtime, authority.stop());
    await expect(runAppEffect(runtime, describedLoad)).resolves.toBe(false);

    expect(loadSnapshot).not.toHaveBeenCalled();
  });

  it("shares one global-all accepted snapshot and replaces it once per invalidation", async () => {
    const loadSnapshot = vi
      .fn<(intent: KataSnapshotIntent) => Promise<KataWorkspaceSnapshotResponse>>()
      .mockImplementationOnce(async (intent) => snapshot(intent))
      .mockImplementationOnce(async (intent) => snapshot(intent, { generation: 2 }));
    const runtime = testRuntime();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot: effectLoader(loadSnapshot) });

    await expect(runAppEffect(runtime, authority.load("home"))).resolves.toBe(true);
    expect(loadSnapshot).toHaveBeenCalledWith({ daemon_id: "home", scope: "global", authority: "all" });
    expect(authority.issues.map((issue) => issue.uid)).toEqual(["issue-1"]);
    await runAppEffect(runtime, authority.refreshIssues("home"));

    expect(loadSnapshot).toHaveBeenCalledTimes(2);
    expect(loadSnapshot.mock.calls[1]?.[0]).toEqual({ daemon_id: "home", scope: "global", authority: "all" });
    expect(authority.issues.map((issue) => issue.uid)).toEqual(["issue-2"]);
    await runAppEffect(runtime, authority.stop());
  });

  it("loads selected enrichment without replacing the shared global-all authority", async () => {
    let generation = 0;
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) =>
      snapshot(intent, {
        generation: ++generation,
        invalidation_epoch: generation,
        event_cursor: generation,
      }),
    );
    const runtime = testRuntime();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot: effectLoader(loadSnapshot) });

    await runAppEffect(runtime, authority.load("home"));
    const selected = await runAppEffect(runtime, authority.selectIssue("issue-selected"));

    expect(loadSnapshot.mock.calls[1]?.[0]).toEqual({
      daemon_id: "home",
      scope: "global",
      authority: "all",
      selected_issue_uid: "issue-selected",
    });
    expect(selected).toMatchObject({
      daemonID: "home",
      detail: { issue: { uid: "issue-selected" }, etag: '"rev-2"' },
    });

    await runAppEffect(runtime, authority.refreshIssues("home"));

    expect(loadSnapshot.mock.calls[2]?.[0]).toEqual({
      daemon_id: "home",
      scope: "global",
      authority: "all",
    });
    await runAppEffect(runtime, authority.stop());
  });

  it("returns each concurrently requested selected issue without superseding either request", async () => {
    const pendingFirst = deferred<KataWorkspaceSnapshotResponse>();
    const pendingSecond = deferred<KataWorkspaceSnapshotResponse>();
    const loadSnapshot = vi.fn(async (requestedIntent: KataSnapshotIntent) => {
      if (requestedIntent.selected_issue_uid === "issue-first") return pendingFirst.promise;
      if (requestedIntent.selected_issue_uid === "issue-second") return pendingSecond.promise;
      return snapshot(requestedIntent);
    });
    const runtime = testRuntime();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot: effectLoader(loadSnapshot) });
    await runAppEffect(runtime, authority.load("home"));

    const firstSelection = runAppEffect(runtime, authority.selectIssue("issue-first"));
    const secondSelection = runAppEffect(runtime, authority.selectIssue("issue-second"));

    await vi.waitFor(() => expect(loadSnapshot).toHaveBeenCalledTimes(3));

    pendingSecond.resolve(
      snapshot(
        { daemon_id: "home", scope: "global", authority: "all", selected_issue_uid: "issue-second" },
        { generation: 3 },
      ),
    );
    await expect(secondSelection).resolves.toMatchObject({ detail: { issue: { uid: "issue-second" } } });

    pendingFirst.resolve(
      snapshot(
        { daemon_id: "home", scope: "global", authority: "all", selected_issue_uid: "issue-first" },
        { generation: 2 },
      ),
    );
    await expect(firstSelection).resolves.toMatchObject({ detail: { issue: { uid: "issue-first" } } });
    await runAppEffect(runtime, authority.stop());
  });

  it("selects against the desired daemon while its base load is still pending", async () => {
    const pendingWork = deferred<KataWorkspaceSnapshotResponse>();
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => {
      if (intent.daemon_id === "work" && !intent.selected_issue_uid) return pendingWork.promise;
      return snapshot(intent, { generation: intent.daemon_id === "work" ? 2 : 1 });
    });
    const runtime = testRuntime();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot: effectLoader(loadSnapshot) });
    await runAppEffect(runtime, authority.load("home"));

    const switching = runAppEffect(runtime, authority.load("work"));
    await vi.waitFor(() => expect(loadSnapshot).toHaveBeenCalledTimes(2));
    const selected = await runAppEffect(runtime, authority.selectIssue("issue-work"));

    expect(loadSnapshot.mock.calls[2]?.[0]).toMatchObject({
      daemon_id: "work",
      selected_issue_uid: "issue-work",
    });
    expect(selected.daemonID).toBe("work");
    pendingWork.resolve(snapshot({ scope: "global", authority: "all", daemon_id: "work" }, { generation: 2 }));
    await expect(switching).resolves.toBe(true);
    await runAppEffect(runtime, authority.stop());
  });

  it("keeps the desired daemon after its base load degrades", async () => {
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => {
      if (intent.daemon_id === "work" && !intent.selected_issue_uid) throw new Error("work unavailable");
      return snapshot(intent, { generation: intent.daemon_id === "work" ? 2 : 1 });
    });
    const runtime = testRuntime();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot: effectLoader(loadSnapshot) });
    await runAppEffect(runtime, authority.load("home"));
    await expect(runAppEffect(runtime, authority.load("work"))).rejects.toThrow("work unavailable");

    const selected = await runAppEffect(runtime, authority.selectIssue("issue-work"));

    expect(loadSnapshot.mock.calls[2]?.[0]).toMatchObject({
      daemon_id: "work",
      selected_issue_uid: "issue-work",
    });
    expect(selected.daemonID).toBe("work");
    await runAppEffect(runtime, authority.stop());
  });

  it("selects against an explicitly requested daemon over the ambient one", async () => {
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => snapshot(intent));
    const runtime = testRuntime();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot: effectLoader(loadSnapshot) });
    await runAppEffect(runtime, authority.load("home"));

    const selected = await runAppEffect(runtime, authority.selectIssue("issue-doc", "docs-daemon"));

    expect(loadSnapshot.mock.calls[1]?.[0]).toMatchObject({
      daemon_id: "docs-daemon",
      selected_issue_uid: "issue-doc",
    });
    expect(selected.daemonID).toBe("docs-daemon");
    await runAppEffect(runtime, authority.stop());
  });

  it("interrupts superseded and active refreshes when the authority stops", async () => {
    const pendingRetry = deferred<KataWorkspaceSnapshotResponse>();
    let loads = 0;
    const loadSnapshot = vi.fn(async (intent: KataSnapshotIntent) => {
      loads += 1;
      if (loads > 1) return pendingRetry.promise;
      return snapshot(intent);
    });
    const runtime = testRuntime();
    const authority = createKataAuxiliaryAuthority({ loadSnapshot: effectLoader(loadSnapshot) });
    await runAppEffect(runtime, authority.load("home"));
    const first = runAppEffect(runtime, authority.refreshIssues("home"));
    await vi.waitFor(() => expect(loadSnapshot).toHaveBeenCalledTimes(2));
    const queued = runAppEffect(runtime, authority.refreshIssues("home"));
    await vi.waitFor(() => expect(loadSnapshot).toHaveBeenCalledTimes(3));
    await runAppEffect(runtime, authority.stop());
    const outcomes = await Promise.allSettled([first, queued]);
    pendingRetry.resolve(snapshot({ scope: "global", authority: "all", daemon_id: "home" }, { generation: 2 }));

    expect(outcomes.map((outcome) => outcome.status)).toEqual(["rejected", "rejected"]);
    expect(loadSnapshot).toHaveBeenCalledTimes(3);
  });
});
