import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import KataWorkspace from "./KataWorkspace.svelte";
import {
  createWorkspaceAPI,
  initialIssues,
  projects,
  recurrence,
  resetKataWorkspaceTestState,
} from "./KataWorkspaceTestSupport.js";

describe("KataWorkspace", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("opens the recurrence editor from the task action menu", async () => {
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
    const { api } = createWorkspaceAPI();

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    const detail = screen.getByRole("region", { name: "Task detail" });

    await fireEvent.click(within(detail).getByRole("button", { name: "More actions" }));
    await fireEvent.click(within(detail).getByRole("menuitem", { name: "Mark as recurring..." }));

    expect(screen.getByRole("dialog", { name: "New recurrence" })).toBeTruthy();
  });

  it("keeps the recurrence editor open when the daemon rejects create", async () => {
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
    const { api, createRecurrence } = createWorkspaceAPI();
    createRecurrence.mockRejectedValueOnce(new Error("daemon rejected recurrence"));

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });
    const detail = screen.getByRole("region", { name: "Task detail" });
    await fireEvent.click(within(detail).getByRole("button", { name: "More actions" }));
    await fireEvent.click(within(detail).getByRole("menuitem", { name: "Mark as recurring..." }));

    const dialog = screen.getByRole("dialog", { name: "New recurrence" });
    await fireEvent.input(within(dialog).getByLabelText("Title"), { target: { value: "Recurring rent" } });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(screen.getByRole("dialog", { name: "New recurrence" })).toBeTruthy();
      expect(screen.getByRole("alert").textContent).toContain("daemon rejected recurrence");
    });
  });

  it("does not show unrelated project recurrences for an attached recurrence miss", async () => {
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
    const rows = initialIssues.map((item) => (item.uid === "issue-pay-rent" ? { ...item, recurrence_id: 99 } : item));
    const { api } = createWorkspaceAPI(rows, {
      recurrences: [recurrence({ id: 1, uid: "recurrence-unrelated", project_id: projects[1]!.id })],
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: "issue-pay-rent" } });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Pay rent" })).toBeTruthy();
    });

    expect(screen.queryByRole("region", { name: "Recurrence" })).toBeNull();
  });
});
