import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import type { ActivitySettings as ActivitySettingsType } from "../../api/types.js";

const { mockHydrateDefaults, mockPersistSettings } = vi.hoisted(() => ({
  mockHydrateDefaults: vi.fn(),
  mockPersistSettings: vi.fn(),
}));

vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({
    activity: { hydrateDefaults: mockHydrateDefaults },
  }),
}));

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      persist: (request) => mockPersistSettings(request),
    }),
  };
});

vi.mock("../../stores/embed-config.svelte.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../stores/embed-config.svelte.js")>()),
  isEmbedded: () => false,
}));

import ActivitySettingsTestHarness from "./ActivitySettingsTestHarness.svelte";

const initial: ActivitySettingsType = {
  view_mode: "threaded",
  time_range: "7d",
  hide_closed: false,
  hide_bots: false,
  collapse_threads: false,
  default_branch_retention_days: 90,
  default_branch_max_commits: 5000,
  use_workspace_activity_for_recency: false,
};

describe("ActivitySettings", () => {
  afterEach(() => {
    cleanup();
    mockHydrateDefaults.mockReset();
    mockPersistSettings.mockReset();
  });

  it("persists and hydrates updated activity defaults", async () => {
    const updated = { ...initial, hide_bots: true };
    const onUpdate = vi.fn();
    mockPersistSettings.mockReturnValue(Effect.succeed({ activity: updated }));
    render(ActivitySettingsTestHarness, { props: { activity: initial, onUpdate } });

    await fireEvent.click(screen.getByRole("button", { name: "Toggle hide bots" }));

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ activity: updated });
    expect(mockHydrateDefaults).toHaveBeenCalledWith(updated);
    expect(onUpdate).toHaveBeenLastCalledWith(updated);
  });

  it("persists the global workspace activity recency preference", async () => {
    const updated = { ...initial, use_workspace_activity_for_recency: true };
    const onUpdate = vi.fn();
    mockPersistSettings.mockReturnValue(Effect.succeed({ activity: updated }));
    render(ActivitySettingsTestHarness, { props: { activity: initial, onUpdate } });

    await fireEvent.click(screen.getByRole("button", { name: "Toggle use workspace activity for recency" }));

    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledOnce());
    expect(mockPersistSettings.mock.calls[0]?.[0]()).toEqual({ activity: updated });
    expect(mockHydrateDefaults).toHaveBeenCalledWith(updated);
  });

  it("restores the previous activity defaults when saving fails", async () => {
    const onUpdate = vi.fn();
    mockPersistSettings.mockReturnValue(
      Effect.fail({ _tag: "TransientTransportError", operation: "save settings", cause: new Error("save failed") }),
    );
    render(ActivitySettingsTestHarness, { props: { activity: initial, onUpdate } });

    await fireEvent.click(screen.getByRole("button", { name: "Toggle hide bots" }));

    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(2));
    expect(onUpdate).toHaveBeenLastCalledWith(initial);
  });

  it("restores the last confirmed defaults when two queued saves fail", async () => {
    let failFirst = () => {};
    let failSecond = () => {};
    const first = new Promise<void>((resolve) => {
      failFirst = resolve;
    });
    const second = new Promise<void>((resolve) => {
      failSecond = resolve;
    });
    const failure = {
      _tag: "TransientTransportError" as const,
      operation: "save settings",
      cause: new Error("save failed"),
    };
    mockPersistSettings
      .mockReturnValueOnce(Effect.promise(() => first).pipe(Effect.andThen(Effect.fail(failure))))
      .mockReturnValueOnce(Effect.promise(() => second).pipe(Effect.andThen(Effect.fail(failure))));
    const onUpdate = vi.fn();
    render(ActivitySettingsTestHarness, { props: { activity: initial, onUpdate } });

    await fireEvent.click(screen.getByRole("button", { name: "Toggle hide bots" }));
    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledTimes(1));
    await fireEvent.click(screen.getByRole("button", { name: "Toggle collapse threads by default" }));
    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledTimes(2));

    failFirst();
    failSecond();

    await waitFor(() => expect(onUpdate).toHaveBeenLastCalledWith(initial));
  });

  it("restores the first confirmed save when the next queued save fails", async () => {
    let completeFirst = () => {};
    let failSecond = () => {};
    const first = new Promise<void>((resolve) => {
      completeFirst = resolve;
    });
    const second = new Promise<void>((resolve) => {
      failSecond = resolve;
    });
    const firstSaved = { ...initial, hide_bots: true };
    const failure = {
      _tag: "TransientTransportError" as const,
      operation: "save settings",
      cause: new Error("save failed"),
    };
    mockPersistSettings
      .mockReturnValueOnce(Effect.promise(() => first).pipe(Effect.as({ activity: firstSaved })))
      .mockReturnValueOnce(Effect.promise(() => second).pipe(Effect.andThen(Effect.fail(failure))));
    const onUpdate = vi.fn();
    render(ActivitySettingsTestHarness, { props: { activity: initial, onUpdate } });

    await fireEvent.click(screen.getByRole("button", { name: "Toggle hide bots" }));
    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledTimes(1));
    await fireEvent.click(screen.getByRole("button", { name: "Toggle collapse threads by default" }));
    await waitFor(() => expect(mockPersistSettings).toHaveBeenCalledTimes(2));

    completeFirst();
    await waitFor(() => expect(mockHydrateDefaults).toHaveBeenCalledWith(firstSaved));
    failSecond();

    await waitFor(() => expect(onUpdate).toHaveBeenLastCalledWith(firstSaved));
  });

  it("uses externally refreshed defaults as the rollback baseline after an earlier save", async () => {
    const firstSaved = { ...initial, hide_bots: true };
    const externallyRefreshed = { ...firstSaved, hide_closed: true };
    const failure = {
      _tag: "TransientTransportError" as const,
      operation: "save settings",
      cause: new Error("save failed"),
    };
    mockPersistSettings
      .mockReturnValueOnce(Effect.succeed({ activity: firstSaved }))
      .mockReturnValueOnce(Effect.fail(failure));
    const onUpdate = vi.fn();
    const rendered = render(ActivitySettingsTestHarness, { props: { activity: initial, onUpdate } });
    await fireEvent.click(screen.getByRole("button", { name: "Toggle hide bots" }));
    await waitFor(() => expect(mockHydrateDefaults).toHaveBeenCalledWith(firstSaved));

    await rendered.rerender({ activity: externallyRefreshed, onUpdate });
    await fireEvent.click(screen.getByRole("button", { name: "Toggle collapse threads by default" }));

    await waitFor(() => expect(onUpdate).toHaveBeenLastCalledWith(externallyRefreshed));
  });
});
