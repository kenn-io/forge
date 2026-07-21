import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import type { KataRecurrence } from "../../api/kata/taskTypes";
import RecurrenceDeleteDialog from "./RecurrenceDeleteDialog.svelte";

afterEach(cleanup);

const sample: KataRecurrence = {
  id: 7,
  uid: "01HZX4Y5Z6A7B8C9D0E1F2G3R1",
  project_id: 1,
  rrule: "FREQ=WEEKLY;COUNT=2",
  dtstart: "2026-05-20",
  timezone: "America/Chicago",
  template_title: "Weekly review",
  template_body: "",
  template_owner: undefined,
  template_priority: undefined,
  template_labels: [],
  template_metadata: {},
  next_occurrence_key: "2026-05-27",
  last_materialized_uid: undefined,
  author: "fixture-user",
  revision: 1,
  created_at: "2026-05-15T12:00:00.000Z",
  updated_at: "2026-05-15T12:00:00.000Z",
};

describe("RecurrenceDeleteDialog", () => {
  test("renders the template title in the body and confirm/cancel buttons", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(RecurrenceDeleteDialog, {
      props: { open: true, recurrence: sample, onConfirm, onCancel },
    });
    expect(screen.getByText("Delete recurrence")).toBeTruthy();
    expect(screen.getByText(/Stop creating new occurrences of/)).toBeTruthy();
    expect(screen.getByText("Weekly review")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Delete" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeTruthy();
  });

  test("Delete fires onConfirm; Cancel fires onCancel", async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(RecurrenceDeleteDialog, {
      props: { open: true, recurrence: sample, onConfirm, onCancel },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  test("authority fencing disables Delete without dismissing the dialog", async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const view = render(RecurrenceDeleteDialog, {
      props: { open: true, recurrence: sample, onConfirm, onCancel },
    });
    await view.rerender({ disabled: true });

    const deleteButton = screen.getByRole("button", { name: "Delete" }) as HTMLButtonElement;
    expect(deleteButton.disabled).toBe(true);
    await fireEvent.click(deleteButton);

    expect(onConfirm).not.toHaveBeenCalled();
    expect(onCancel).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Delete recurrence" })).toBeTruthy();

    await view.rerender({ disabled: false });
    expect(deleteButton.disabled).toBe(false);
    await fireEvent.click(deleteButton);
    expect(onConfirm).toHaveBeenCalledOnce();
  });

  test("does not render when open=false", () => {
    render(RecurrenceDeleteDialog, {
      props: { open: false, recurrence: sample, onConfirm: () => {}, onCancel: () => {} },
    });
    expect(screen.queryByText("Delete recurrence")).toBeNull();
  });
});
