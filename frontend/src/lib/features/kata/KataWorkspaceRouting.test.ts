import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataWorkspaceSnapshotResponse } from "../../api/kata/snapshot.js";
import KataWorkspace from "./KataWorkspace.svelte";
import {
  createWorkspaceAPI,
  deferred,
  initialIssues,
  resetKataWorkspaceTestState,
} from "./test/KataWorkspaceSupport.js";

function acceptHomeDaemon(): void {
  vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
    Response.json({
      daemons: [
        {
          id: "home",
          url: "http://127.0.0.1:7777",
          default: true,
          auth: "none",
          health: "connected",
        },
      ],
    }),
  );
}

describe("KataWorkspace snapshot routing", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("selects the routed task through snapshot enrichment without a direct detail read", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();
    api.issue = vi.fn(async () => {
      throw new Error("legacy detail read");
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: initialIssues[1]!.uid } });

    await screen.findByRole("heading", { name: initialIssues[1]!.title });
    expect(api.issue).not.toHaveBeenCalled();
  });

  it("updates and clears selected enrichment when route identity changes", async () => {
    acceptHomeDaemon();
    const requests: Array<string | undefined> = [];
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (request, snapshot) => {
        requests.push(request.selectedIssueUID);
        return snapshot;
      },
    });
    const { rerender } = render(KataWorkspace, { props: { api, selectedIssueUID: initialIssues[0]!.uid } });

    await screen.findByRole("heading", { name: initialIssues[0]!.title });
    await rerender({ api, selectedIssueUID: initialIssues[1]!.uid });
    await screen.findByRole("heading", { name: initialIssues[1]!.title });
    await rerender({ api, selectedIssueUID: null });
    await screen.findByText("Select a task");

    expect(requests).toEqual([initialIssues[0]!.uid, initialIssues[1]!.uid, undefined]);
  });

  it("notifies after a row-selected snapshot is accepted", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspace, { props: { api, onSelectedIssueChange } });

    await screen.findByRole("button", { name: new RegExp(initialIssues[1]!.title) });
    await fireEvent.click(screen.getByRole("button", { name: new RegExp(initialIssues[1]!.title) }));

    await screen.findByRole("heading", { name: initialIssues[1]!.title });
    await waitFor(() => expect(onSelectedIssueChange).toHaveBeenCalledWith(initialIssues[1]!.uid));
  });

  it("does not publish a superseded row selection", async () => {
    acceptHomeDaemon();
    const slowEmail = deferred<KataWorkspaceSnapshotResponse>();
    let pendingEmailSnapshot: KataWorkspaceSnapshotResponse | null = null;
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: async (request, snapshot) => {
        if (request.selectedIssueUID === initialIssues[1]!.uid) {
          pendingEmailSnapshot = snapshot;
          return slowEmail.promise;
        }
        return snapshot;
      },
    });
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspace, { props: { api, onSelectedIssueChange } });
    await screen.findByRole("button", { name: new RegExp(initialIssues[1]!.title) });

    await fireEvent.click(screen.getByRole("button", { name: new RegExp(initialIssues[1]!.title) }));
    await fireEvent.click(screen.getByRole("button", { name: new RegExp(initialIssues[0]!.title) }));
    await waitFor(() => expect(onSelectedIssueChange).toHaveBeenCalledWith(initialIssues[0]!.uid));

    expect(pendingEmailSnapshot).not.toBeNull();
    slowEmail.resolve(pendingEmailSnapshot!);
    await Promise.resolve();
    expect(onSelectedIssueChange).not.toHaveBeenCalledWith(initialIssues[1]!.uid);
    expect(screen.getByRole("heading", { name: initialIssues[0]!.title })).toBeTruthy();
  });
});
