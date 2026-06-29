import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { KataTaskDetail } from "../../api/kata/taskTypes.js";

import KataIssueProperties from "./KataIssueProperties.svelte";

function makeIssue(overrides: Partial<KataTaskDetail["issue"]> = {}): KataTaskDetail {
  return {
    issue: {
      id: 1,
      uid: "issue-1",
      project_id: 1,
      project_uid: "project-1",
      project_name: "Inbox",
      short_id: "I-1",
      qualified_id: "INBOX-1",
      title: "Ship the thing",
      body: "Body",
      status: "open",
      metadata: {},
      revision: 1,
      author: "wes",
      created_at: "2026-06-01T12:00:00Z",
      updated_at: "2026-06-01T12:00:00Z",
      ...overrides,
    },
    comments: [],
    labels: [{ issue_id: 1, label: "review", author: "wes", created_at: "2026-06-01T12:00:00Z" }],
    links: [],
  };
}

const ownerOptions = [
  { value: "fixture-user", label: "fixture-user" },
  { value: "agent:planner", label: "agent:planner" },
];

function renderProperties(
  props: Partial<{
    issue: KataTaskDetail;
    ownerOptions: typeof ownerOptions;
    onPatchMetadata: (uid: string, patch: Record<string, unknown>) => boolean | Promise<boolean>;
    onAssignOwner: (uid: string, owner: string) => boolean | Promise<boolean>;
    onUnassignOwner: (uid: string) => boolean | Promise<boolean>;
    onSetPriority: (uid: string, priority: number | null) => boolean | Promise<boolean>;
    onAddLabel: (uid: string, label: string) => boolean | Promise<boolean>;
    onRemoveLabel: (uid: string, label: string) => void | Promise<void>;
  }> = {},
) {
  return render(KataIssueProperties, {
    props: {
      issue: makeIssue({
        owner: "fixture-user",
        metadata: { scheduled_on: "2026-06-01", deadline_on: "2026-06-05" },
      }),
      ownerOptions,
      onPatchMetadata: vi.fn(async () => true),
      onAssignOwner: vi.fn(async () => true),
      onUnassignOwner: vi.fn(async () => true),
      onSetPriority: vi.fn(async () => true),
      onAddLabel: vi.fn(async () => true),
      onRemoveLabel: vi.fn(),
      ...props,
    },
  });
}

describe("KataIssueProperties", () => {
  afterEach(() => {
    cleanup();
  });

  it("adds and removes labels", async () => {
    const onAddLabel = vi.fn(async () => true);
    const onRemoveLabel = vi.fn();

    render(KataIssueProperties, {
      props: {
        issue: makeIssue({ metadata: { deadline_on: "2026-06-05" } }),
        ownerOptions: [],
        onPatchMetadata: vi.fn(async () => true),
        onAssignOwner: vi.fn(async () => true),
        onUnassignOwner: vi.fn(async () => true),
        onSetPriority: vi.fn(async () => true),
        onAddLabel,
        onRemoveLabel,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Add label" }));
    await fireEvent.input(screen.getByLabelText("New label"), {
      target: { value: "blocked" },
    });
    await fireEvent.keyDown(screen.getByLabelText("New label"), { key: "Enter" });

    expect(onAddLabel).toHaveBeenCalledWith("issue-1", "blocked");

    await fireEvent.click(screen.getByRole("button", { name: "Edit labels" }));
    await fireEvent.click(screen.getByRole("button", { name: "Remove label review" }));

    expect(onRemoveLabel).toHaveBeenCalledWith("issue-1", "review");
  });

  it("keeps labels passive until label editing is enabled", async () => {
    const onRemoveLabel = vi.fn();
    renderProperties({ onRemoveLabel });

    const labels = screen.getByRole("list", { name: "Labels" });
    const labelText = within(labels).getByText("review");
    expect(labelText.closest("button")).toBeNull();
    expect(within(labels).queryByRole("button", { name: "Remove label review" })).toBeNull();

    await fireEvent.click(labelText);
    expect(onRemoveLabel).not.toHaveBeenCalled();

    await fireEvent.click(screen.getByRole("button", { name: "Edit labels" }));
    const removeButton = within(labels).getByRole("button", { name: "Remove label review" });
    expect(removeButton.classList.contains("chip")).toBe(true);
    expect(removeButton.classList.contains("kata-label-chip")).toBe(true);
    expect(removeButton.textContent).toContain("review");
    await fireEvent.click(removeButton);
    expect(onRemoveLabel).toHaveBeenCalledWith("issue-1", "review");

    await fireEvent.click(screen.getByRole("button", { name: "Done editing labels" }));
    expect(within(labels).queryByRole("button", { name: "Remove label review" })).toBeNull();
  });

  it("keeps the label draft visible when label creation fails", async () => {
    renderProperties({
      onAddLabel: vi.fn(async () => false),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Add label" }));
    await fireEvent.input(screen.getByLabelText("New label"), { target: { value: "urgent" } });
    await fireEvent.keyDown(screen.getByLabelText("New label"), { key: "Enter" });

    expect((screen.getByLabelText("New label") as HTMLInputElement).value).toBe("urgent");
  });

  it("persists due date and priority changes", async () => {
    const onPatchMetadata = vi.fn(async () => true);
    const onSetPriority = vi.fn(async () => true);

    render(KataIssueProperties, {
      props: {
        issue: makeIssue({ metadata: { deadline_on: "2026-06-05" } }),
        ownerOptions: [],
        onPatchMetadata,
        onAssignOwner: vi.fn(async () => true),
        onUnassignOwner: vi.fn(async () => true),
        onSetPriority,
        onAddLabel: vi.fn(async () => true),
        onRemoveLabel: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit due date" }));
    await fireEvent.click(screen.getByRole("button", { name: /Due:/ }));
    await fireEvent.click(screen.getByRole("button", { name: /Monday, June 8, 2026/ }));

    expect(onPatchMetadata).toHaveBeenCalledWith("issue-1", { deadline_on: "2026-06-08" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit priority" }));
    await fireEvent.change(screen.getByLabelText("Priority"), {
      target: { value: "1" },
    });

    expect(onSetPriority).toHaveBeenCalledWith("issue-1", 1);
  });

  it("patches scheduled and due dates and closes editors on success", async () => {
    const onPatchMetadata = vi.fn(async () => true);
    renderProperties({ onPatchMetadata });

    await fireEvent.click(screen.getByRole("button", { name: "Edit scheduled" }));
    await fireEvent.click(screen.getByRole("button", { name: /Scheduled:/ }));
    await fireEvent.click(screen.getByRole("button", { name: /Wednesday, June 10, 2026/ }));

    expect(onPatchMetadata).toHaveBeenCalledWith("issue-1", { scheduled_on: "2026-06-10" });
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Scheduled:/ })).toBeNull();
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit due date" }));
    await fireEvent.click(screen.getByRole("button", { name: /Due:/ }));
    await fireEvent.click(screen.getByRole("button", { name: /Monday, June 8, 2026/ }));

    expect(onPatchMetadata).toHaveBeenCalledWith("issue-1", { deadline_on: "2026-06-08" });
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Due:/ })).toBeNull();
    });
  });

  it("clears date properties and closes date editors on Escape", async () => {
    const onPatchMetadata = vi.fn(async () => true);
    renderProperties({ onPatchMetadata });

    await fireEvent.click(screen.getByRole("button", { name: "Edit scheduled" }));
    const scheduled = screen.getByRole("button", { name: /Scheduled: Jun 1/ });
    await fireEvent.keyDown(scheduled, { key: "Escape" });

    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Scheduled:/ })).toBeNull();
      expect(within(screen.getByRole("button", { name: "Edit scheduled" })).getByText("Jun 1")).toBeTruthy();
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit scheduled" }));
    await fireEvent.click(screen.getByRole("button", { name: "Clear scheduled" }));

    expect(onPatchMetadata).toHaveBeenCalledWith("issue-1", { scheduled_on: null });

    await fireEvent.click(screen.getByRole("button", { name: "Edit due date" }));
    expect(screen.getByRole("button", { name: /Due: Jun 5/ })).toBeTruthy();
    await fireEvent.keyDown(screen.getByRole("button", { name: "Clear due date" }), { key: "Escape" });

    await waitFor(() => {
      expect(screen.queryByRole("button", { name: /Due:/ })).toBeNull();
      expect(within(screen.getByRole("button", { name: "Edit due date" })).getByText("Jun 5")).toBeTruthy();
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit due date" }));
    await fireEvent.click(screen.getByRole("button", { name: "Clear due date" }));

    expect(onPatchMetadata).toHaveBeenCalledWith("issue-1", { deadline_on: null });
  });

  it("keeps the property editor open when a property mutation fails", async () => {
    renderProperties({
      onPatchMetadata: vi.fn(async () => false),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit scheduled" }));
    await fireEvent.click(screen.getByRole("button", { name: /Scheduled:/ }));
    await fireEvent.click(screen.getByRole("button", { name: /Wednesday, June 10, 2026/ }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Scheduled: Jun 10/ })).toBeTruthy();
    });
  });

  it("assigns highlighted, custom, and unassigned owners", async () => {
    const onAssignOwner = vi.fn(async () => true);
    const onUnassignOwner = vi.fn(async () => true);
    renderProperties({ onAssignOwner, onUnassignOwner });

    await fireEvent.click(screen.getByRole("button", { name: "Owner: fixture-user" }));
    const ownerInput = screen.getByRole("combobox", { name: "Owner" });
    expect(ownerInput).toBe(document.activeElement);
    await fireEvent.input(ownerInput, { target: { value: "planner" } });
    await fireEvent.keyDown(ownerInput, { key: "Enter" });

    expect(onAssignOwner).toHaveBeenCalledWith("issue-1", "agent:planner");

    await fireEvent.click(screen.getByRole("button", { name: "Owner: fixture-user" }));
    await fireEvent.input(screen.getByRole("combobox", { name: "Owner" }), { target: { value: "agent:new" } });
    await fireEvent.keyDown(screen.getByRole("combobox", { name: "Owner" }), { key: "Enter" });

    expect(onAssignOwner).toHaveBeenCalledWith("issue-1", "agent:new");

    await fireEvent.click(screen.getByRole("button", { name: "Owner: fixture-user" }));
    await fireEvent.mouseDown(screen.getByRole("option", { name: "Unassigned" }));

    expect(onUnassignOwner).toHaveBeenCalledWith("issue-1");
  });

  it("keeps custom owner text visible when owner assignment fails", async () => {
    renderProperties({
      onAssignOwner: vi.fn(async () => false),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Owner: fixture-user" }));
    const ownerInput = screen.getByRole("combobox", { name: "Owner" }) as HTMLInputElement;
    await fireEvent.input(ownerInput, { target: { value: "agent:new" } });
    await fireEvent.keyDown(ownerInput, { key: "Enter" });

    expect(screen.getByRole("combobox", { name: "Owner" })).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "Owner" }) as HTMLInputElement).value).toBe("agent:new");
  });

  it("clears the priority through the detail property control", async () => {
    const onSetPriority = vi.fn(async () => true);
    renderProperties({ issue: makeIssue({ priority: 2 }), onSetPriority });

    await fireEvent.click(screen.getByRole("button", { name: "Edit priority" }));
    await fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "" } });

    expect(onSetPriority).toHaveBeenCalledWith("issue-1", null);
  });
});
