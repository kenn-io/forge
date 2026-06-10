import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

vi.mock("./KataWorkspace.svelte", async () => ({
  default: (await import("./KataWorkspaceTestStub.svelte")).default,
}));

import type { KataTaskAPI } from "../../api/kata/taskTypes.js";
import KataFeature from "./KataFeature.svelte";

describe("KataFeature", () => {
  afterEach(() => {
    cleanup();
  });

  it("passes route, api, and callbacks through to the Kata workspace", async () => {
    const api = {} as KataTaskAPI;
    const onSelectedIssueChange = vi.fn();
    const onRouteStateChange = vi.fn();
    const onOpenMessage = vi.fn();

    render(KataFeature, {
      props: {
        api,
        selectedIssueUID: "issue-current",
        routeViewName: "inbox",
        routeScopeUID: "project-a",
        onSelectedIssueChange,
        onRouteStateChange,
        onOpenMessage,
      },
    });

    const stub = screen.getByTestId("kata-workspace-stub");
    expect(stub.dataset.hasApi).toBe("true");
    expect(stub.dataset.selectedIssue).toBe("issue-current");
    expect(stub.dataset.routeView).toBe("inbox");
    expect(stub.dataset.routeScope).toBe("project-a");

    await fireEvent.click(screen.getByRole("button", { name: "select" }));
    await fireEvent.click(screen.getByRole("button", { name: "route" }));
    await fireEvent.click(screen.getByRole("button", { name: "message" }));

    expect(onSelectedIssueChange).toHaveBeenCalledWith("issue-next");
    expect(onRouteStateChange).toHaveBeenCalledWith({ view: "today", scope: "project-a", issue: "issue-next" });
    expect(onOpenMessage).toHaveBeenCalledWith(42);
  });
});
