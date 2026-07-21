import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import KataWorkspace from "./KataWorkspace.svelte";
import {
  createWorkspaceAPI,
  initialIssues,
  projects,
  recurrence,
  resetKataWorkspaceTestState,
} from "./test/KataWorkspaceSupport.js";

async function waitForWorkspaceWritable(): Promise<void> {
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false),
  );
}

describe("KataWorkspace", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("loads recurrences separately after selected snapshot acceptance", async () => {
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
    const selected = { ...initialIssues[0]!, recurrence_id: 41 };
    const recurrenceRow = recurrence({
      id: 41,
      uid: "recurrence-selected",
      project_id: selected.project_id,
      template_title: "Snapshot recurrence",
    });
    const { api, recurrences: recurrenceReads } = createWorkspaceAPI([selected], { recurrences: [recurrenceRow] });

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: selected.title });
    expect(recurrenceReads).toHaveBeenCalledWith(selected.project_id, expect.objectContaining({ daemonId: "home" }));
    expect(screen.getByRole("region", { name: "Recurrence" })).toBeTruthy();
    expect(screen.getByText("Snapshot recurrence")).toBeTruthy();
  });

  it("keeps recurrence reads and CRUD operational without the legacy workspace authority store", async () => {
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
    const selected = initialIssues[0]!;
    const recurrenceRow = recurrence({
      id: 41,
      uid: "recurrence-selected",
      project_id: selected.project_id,
      template_title: "Snapshot recurrence",
    });
    const {
      api,
      recurrences: recurrenceReads,
      createRecurrence,
      patchRecurrence,
      deleteRecurrence,
    } = createWorkspaceAPI([selected], {
      recurrences: [recurrenceRow],
    });
    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: selected.title });
    await waitFor(() => expect(recurrenceReads).toHaveBeenCalledOnce());

    await fireEvent.click(screen.getByRole("button", { name: "Snapshot recurrence" }));
    const editDialog = screen.getByRole("dialog", { name: "Edit recurrence" });
    await fireEvent.input(within(editDialog).getByLabelText("Title"), {
      target: { value: "Edited recurrence" },
    });
    await fireEvent.click(within(editDialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patchRecurrence).toHaveBeenCalledOnce());

    await fireEvent.click(screen.getByRole("button", { name: "Delete recurrence" }));
    const deleteDialog = screen.getByRole("dialog", { name: "Delete recurrence" });
    await fireEvent.click(within(deleteDialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(deleteRecurrence).toHaveBeenCalledOnce());

    await fireEvent.click(screen.getByRole("button", { name: "+ New recurrence" }));
    const createDialog = screen.getByRole("dialog", { name: "New recurrence" });
    await fireEvent.input(within(createDialog).getByLabelText("Title"), {
      target: { value: "Created recurrence" },
    });
    await fireEvent.click(within(createDialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(createRecurrence).toHaveBeenCalledOnce());
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
    await waitForWorkspaceWritable();
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
    await waitForWorkspaceWritable();
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
