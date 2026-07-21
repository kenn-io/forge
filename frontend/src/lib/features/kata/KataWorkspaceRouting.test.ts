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

  it("selects the routed task through snapshot enrichment", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api, selectedIssueUID: initialIssues[1]!.uid } });

    await screen.findByRole("heading", { name: initialIssues[1]!.title });
  });

  it("clears an initially routed task that accepted snapshot authority cannot select", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, {
      props: { api, selectedIssueUID: "issue-missing", onRouteStateChange },
    });

    await screen.findByText("Select a task");
    await waitFor(() => expect(onRouteStateChange).toHaveBeenCalledWith({ issue: null }, { replace: true }));
  });

  it("clears a newly routed task that accepted snapshot authority cannot select", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();
    const onRouteStateChange = vi.fn();
    const { rerender } = render(KataWorkspace, {
      props: { api, selectedIssueUID: initialIssues[0]!.uid, onRouteStateChange },
    });

    await screen.findByRole("heading", { name: initialIssues[0]!.title });
    await rerender({ api, selectedIssueUID: "issue-missing", onRouteStateChange });

    await screen.findByText("Select a task");
    await waitFor(() => expect(onRouteStateChange).toHaveBeenCalledWith({ issue: null }, { replace: true }));
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

  it("combines a routed project scope with its routed system view", async () => {
    acceptHomeDaemon();
    const onRouteStateChange = vi.fn();
    const deadline = {
      ...initialIssues[1]!,
      uid: "issue-project-deadline",
      short_id: "project-deadline",
      qualified_id: "Kata#project-deadline",
      title: "Project deadline",
      metadata: { deadline_on: "9999-12-31" },
    };
    const { api } = createWorkspaceAPI([...initialIssues, deadline]);

    render(KataWorkspace, {
      props: {
        api,
        routeViewName: "deadlines",
        routeScopeUID: "project-kata",
        onRouteStateChange,
      },
    });

    await screen.findByRole("button", { name: /Project deadline/ });
    expect(screen.queryByRole("button", { name: /Email Susan re: Q3/ })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /Project scope: Kata/ }));
    await fireEvent.mouseDown(screen.getByRole("option", { name: "All projects" }));

    await waitFor(() => expect(screen.getByRole("button", { name: /Project scope: All projects/ })).toBeTruthy());
    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenCalledWith({ view: "deadlines", scope: null, issue: null }),
    );
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

  it("does not clear the prior route while a newly selected row is being accepted", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();
    const onSelectedIssueChange = vi.fn();
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, {
      props: {
        api,
        selectedIssueUID: initialIssues[0]!.uid,
        onSelectedIssueChange,
        onRouteStateChange,
      },
    });

    await screen.findByRole("heading", { name: initialIssues[0]!.title });
    await fireEvent.click(screen.getByRole("button", { name: new RegExp(initialIssues[1]!.title) }));

    await waitFor(() => expect(onSelectedIssueChange).toHaveBeenCalledWith(initialIssues[1]!.uid));
    expect(onRouteStateChange).not.toHaveBeenCalledWith({ issue: null }, { replace: true });
  });

  it("replaces the cleared project-scope entry with its first accepted row selection", async () => {
    acceptHomeDaemon();
    const { api } = createWorkspaceAPI();
    const onSelectedIssueChange = vi.fn();
    const onRouteStateChange = vi.fn();

    render(KataWorkspace, {
      props: {
        api,
        selectedIssueUID: initialIssues[1]!.uid,
        routeViewName: "deadlines",
        routeScopeUID: "project-kata",
        onSelectedIssueChange,
        onRouteStateChange,
      },
    });

    await screen.findByRole("heading", { name: initialIssues[1]!.title });
    await fireEvent.click(screen.getByRole("button", { name: /^Finances\s+1$/ }));
    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenCalledWith({ view: null, scope: "project-finances", issue: null }),
    );

    await fireEvent.click(screen.getByRole("button", { name: new RegExp(initialIssues[0]!.title) }));
    await waitFor(() =>
      expect(onRouteStateChange).toHaveBeenCalledWith({ issue: initialIssues[0]!.uid }, { replace: true }),
    );
    expect(onSelectedIssueChange).not.toHaveBeenCalledWith(initialIssues[0]!.uid);
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
