import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import KataWorkspace from "./KataWorkspace.svelte";
import {
  createWorkspaceAPI,
  deferred,
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
      revision: 6,
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
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit recurrence" })).toBeNull());

    await fireEvent.click(screen.getByRole("button", { name: "Delete recurrence" }));
    const deleteDialog = screen.getByRole("dialog", { name: "Delete recurrence" });
    await fireEvent.click(within(deleteDialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(deleteRecurrence).toHaveBeenCalledOnce());
    expect(deleteRecurrence).toHaveBeenCalledWith(
      selected.project_id,
      "recurrence-selected",
      "middleman",
      expect.any(Object),
      '"rev-6"',
    );
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete recurrence" })).toBeNull());

    await fireEvent.click(screen.getByRole("button", { name: "+ New recurrence" }));
    const createDialog = screen.getByRole("dialog", { name: "New recurrence" });
    await fireEvent.input(within(createDialog).getByLabelText("Title"), {
      target: { value: "Created recurrence" },
    });
    await fireEvent.click(within(createDialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(createRecurrence).toHaveBeenCalledOnce());
  });

  it("sends the recurrence revision and reloads the list on a delete conflict", async () => {
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
      revision: 9,
    });
    const {
      api,
      recurrences: recurrenceReads,
      deleteRecurrence,
    } = createWorkspaceAPI([selected], {
      recurrences: [recurrenceRow],
    });
    deleteRecurrence.mockRejectedValueOnce(Object.assign(new Error("recurrence revision conflict"), { status: 412 }));
    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: selected.title });
    await waitFor(() => expect(recurrenceReads).toHaveBeenCalledOnce());

    await fireEvent.click(screen.getByRole("button", { name: "Delete recurrence" }));
    const deleteDialog = screen.getByRole("dialog", { name: "Delete recurrence" });
    await fireEvent.click(within(deleteDialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(deleteRecurrence).toHaveBeenCalledOnce());
    expect(deleteRecurrence).toHaveBeenCalledWith(
      selected.project_id,
      "recurrence-selected",
      "middleman",
      expect.any(Object),
      '"rev-9"',
    );
    await waitFor(() => expect(recurrenceReads).toHaveBeenCalledTimes(2));
  });

  it("retries a conflicted recurrence delete with the reloaded revision", async () => {
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
    const staleRow = recurrence({
      id: 41,
      uid: "recurrence-selected",
      project_id: selected.project_id,
      template_title: "Snapshot recurrence",
      revision: 9,
    });
    const freshRow = { ...staleRow, revision: 10 };
    const {
      api,
      recurrences: recurrenceReads,
      deleteRecurrence,
    } = createWorkspaceAPI([selected], {
      recurrences: [freshRow],
    });
    recurrenceReads.mockResolvedValueOnce({ recurrences: [staleRow], fetched_at: "2026-02-11T08:00:00Z" });
    deleteRecurrence.mockRejectedValueOnce(Object.assign(new Error("recurrence revision conflict"), { status: 412 }));
    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: selected.title });
    await waitFor(() => expect(recurrenceReads).toHaveBeenCalledOnce());

    await fireEvent.click(screen.getByRole("button", { name: "Delete recurrence" }));
    const deleteDialog = screen.getByRole("dialog", { name: "Delete recurrence" });
    await fireEvent.click(within(deleteDialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(deleteRecurrence).toHaveBeenCalledOnce());
    expect(deleteRecurrence).toHaveBeenLastCalledWith(
      selected.project_id,
      "recurrence-selected",
      "middleman",
      expect.any(Object),
      '"rev-9"',
    );
    await waitFor(() => expect(recurrenceReads).toHaveBeenCalledTimes(2));

    expect(screen.getByRole("dialog", { name: "Delete recurrence" })).toBeTruthy();
    await fireEvent.click(within(deleteDialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(deleteRecurrence).toHaveBeenCalledTimes(2));
    expect(deleteRecurrence).toHaveBeenLastCalledWith(
      selected.project_id,
      "recurrence-selected",
      "middleman",
      expect.any(Object),
      '"rev-10"',
    );
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete recurrence" })).toBeNull());
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

  it("keeps recurrence mutations fenced until the auxiliary recurrence refresh succeeds", async () => {
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
    const { api, recurrences, deleteRecurrence } = createWorkspaceAPI([selected], {
      recurrences: [recurrenceRow],
    });
    recurrences
      .mockResolvedValueOnce({ recurrences: [recurrenceRow], fetched_at: "2026-06-01T12:00:00Z" })
      .mockRejectedValueOnce(new Error("recurrence refresh failed"))
      .mockResolvedValueOnce({ recurrences: [], fetched_at: "2026-06-01T12:00:00Z" });

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("button", { name: recurrenceRow.template_title });
    await fireEvent.click(screen.getByRole("button", { name: "Delete recurrence" }));
    await fireEvent.click(
      within(screen.getByRole("dialog", { name: "Delete recurrence" })).getByRole("button", {
        name: "Delete",
      }),
    );

    await waitFor(() => expect(deleteRecurrence).toHaveBeenCalledOnce());
    await waitFor(() => expect(recurrences).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("saved"));
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(true);

    await fireEvent.click(screen.getByRole("button", { name: "Retry Kata snapshot" }));
    await waitFor(() => expect(recurrences).toHaveBeenCalledTimes(3));
    await waitForWorkspaceWritable();
    expect(deleteRecurrence).toHaveBeenCalledOnce();
  });

  it("preserves recurrence refresh ownership when selection changes during a slow acknowledgement", async () => {
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
    const source = initialIssues[0]!;
    const target = initialIssues[1]!;
    const recurrenceRow = recurrence({
      id: 41,
      uid: "recurrence-selected",
      project_id: source.project_id,
      template_title: "Snapshot recurrence",
    });
    const mutation = deferred<void>();
    const postAcknowledgementRecurrences = deferred<{
      recurrences: ReturnType<typeof recurrence>[];
      fetched_at: string;
    }>();
    const { api, recurrences, deleteRecurrence } = createWorkspaceAPI([source, target], {
      recurrences: [recurrenceRow],
    });
    deleteRecurrence.mockImplementationOnce(() => mutation.promise);
    recurrences
      .mockResolvedValueOnce({ recurrences: [recurrenceRow], fetched_at: "2026-06-01T12:00:00Z" })
      .mockResolvedValueOnce({ recurrences: [], fetched_at: "2026-06-01T12:00:00Z" })
      .mockImplementationOnce(() => postAcknowledgementRecurrences.promise);

    const view = render(KataWorkspace, { props: { api, selectedIssueUID: source.uid } });

    await screen.findByRole("button", { name: recurrenceRow.template_title });
    await fireEvent.click(screen.getByRole("button", { name: "Delete recurrence" }));
    await fireEvent.click(
      within(screen.getByRole("dialog", { name: "Delete recurrence" })).getByRole("button", {
        name: "Delete",
      }),
    );
    await waitFor(() => expect(deleteRecurrence).toHaveBeenCalledOnce());

    await view.rerender({ selectedIssueUID: target.uid });
    await screen.findByRole("heading", { name: target.title });
    await waitFor(() => expect(recurrences).toHaveBeenCalledTimes(2));

    mutation.resolve();
    await waitFor(() => expect(recurrences).toHaveBeenCalledTimes(3));
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(true);

    postAcknowledgementRecurrences.resolve({
      recurrences: [],
      fetched_at: "2026-06-01T12:00:00Z",
    });
    await waitForWorkspaceWritable();
    expect(deleteRecurrence).toHaveBeenCalledOnce();
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
