import { Effect } from "effect";
import { afterEach, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../app/runtime.js";
import { createMockApiFetch } from "../../test/mockApiFetch.js";
import { dismissFlash, getFlashes } from "./flash.svelte.js";
import { stopSessionsInWorkspaces } from "./workspace-sessions-stop.js";

afterEach(() => {
  for (const flash of getFlashes()) dismissFlash(flash.id);
  vi.unstubAllGlobals();
});

function session(key: string, kind: string) {
  return {
    key,
    kind,
    label: key,
    target_key: kind,
    workspace_id: "ws",
    status: "running",
    display_region: "workflow",
    created_at: "2026-05-12T12:00:00Z",
  };
}

it("stops every session, counts already-gone sessions as stopped elsewhere, and reports the rest", async () => {
  const stopped: string[] = [];
  const sessions: Record<string, ReturnType<typeof session>[]> = {
    "ws-a": [session("agent-1", "agent"), session("chat-1", "acp"), session("gone-1", "shell")],
    "ws-b": [session("stuck-1", "agent")],
  };
  const api = createMockApiFetch([
    ({ method, url }) => {
      const runtime = /^\/api\/v1\/workspaces\/([^/]+)\/runtime$/.exec(url.pathname);
      if (method === "GET" && runtime) {
        return Response.json({ sessions: sessions[runtime[1]!] ?? [], launch_targets: [] });
      }
      const stop = /^\/api\/v1\/workspaces\/([^/]+)\/runtime\/sessions\/([^/]+)$/.exec(url.pathname);
      if (method === "DELETE" && stop) {
        const key = stop[2]!;
        if (key === "gone-1")
          return Response.json({ code: "notFound", title: "Not Found", status: 404 }, { status: 404 });
        if (key === "stuck-1") {
          return Response.json(
            { code: "internalError", title: "Internal", detail: "stop runtime session: busy", status: 500 },
            { status: 500 },
          );
        }
        stopped.push(`${stop[1]}/${key}`);
        return new Response(null, { status: 204 });
      }
      return undefined;
    },
  ]);
  vi.stubGlobal("fetch", api.fetch);
  const runtime = makeAppRuntime();
  const done = vi.fn();
  try {
    stopSessionsInWorkspaces(
      runtime,
      [
        { workspaceId: "ws-a", label: "Alpha" },
        { workspaceId: "ws-b", label: "Beta" },
      ],
      done,
    );
    await vi.waitFor(() => expect(getFlashes()).toHaveLength(1));
    expect(stopped.toSorted()).toEqual(["ws-a/agent-1", "ws-a/chat-1"]);
    expect(done.mock.calls.map(([workspace]) => workspace.label).toSorted()).toEqual(["Alpha", "Beta"]);
    expect(getFlashes()[0]).toMatchObject({
      tone: "warning",
      message:
        "Stopped 2 sessions in 2 workspaces, but some could not be stopped. Beta: stuck-1: stop runtime session: busy.",
    });
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
  }
});

it("says so when a workspace has no sessions to stop", async () => {
  const api = createMockApiFetch([
    ({ method, url }) =>
      method === "GET" && url.pathname === "/api/v1/workspaces/ws-idle/runtime"
        ? Response.json({ sessions: [], launch_targets: [] })
        : undefined,
  ]);
  vi.stubGlobal("fetch", api.fetch);
  const runtime = makeAppRuntime();
  try {
    stopSessionsInWorkspaces(runtime, [{ workspaceId: "ws-idle", label: "Idle" }], () => {});
    await vi.waitFor(() =>
      expect(getFlashes()).toEqual([
        expect.objectContaining({ tone: "success", message: 'No sessions were running in "Idle".' }),
      ]),
    );
  } finally {
    await Effect.runPromise(runtime.disposeEffect);
  }
});
