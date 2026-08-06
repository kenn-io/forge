import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect, Layer } from "effect";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import * as flash from "../../stores/flash.svelte.js";

import type { FleetSettings as FleetSettingsType } from "../../api/types.js";

const { mockUpdateFleetSettings } = vi.hoisted(() => ({
  mockUpdateFleetSettings: vi.fn(),
}));

vi.mock("../../stores/settings-workflow.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../stores/settings-workflow.js")>();
  return {
    ...actual,
    SettingsWorkflowLive: Layer.mock(actual.SettingsWorkflow)({
      updateFleet: (request) => mockUpdateFleetSettings(request),
    }),
  };
});

vi.mock("../../stores/embed-config.svelte.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../stores/embed-config.svelte.js")>()),
  isEmbedded: () => false,
}));

import FleetSettings from "./FleetSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

afterEach(() => {
  for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
});

function fleetSettings(overrides: Partial<FleetSettingsType> = {}): FleetSettingsType {
  return {
    enabled: false,
    key: "studio",
    peer_timeout: "2s",
    sessions: { include_unmanaged_details: false },
    peers: [
      {
        key: "mini",
        name: "Mac mini",
        base_url: "http://mini.tail:8091",
      },
    ],
    ssh_peers: [
      {
        key: "epyc",
        name: "EPYC",
        destination: "wes@epyc.tail",
        platform: "linux",
      },
    ],
    restart_required: false,
    ...overrides,
  };
}

function renderFleetSettings(fleet: FleetSettingsType, onUpdate = vi.fn()): void {
  render(SettingsRuntimeHarness, {
    props: { component: FleetSettings, componentProps: { fleet, onUpdate } },
  });
}

describe("FleetSettings", () => {
  afterEach(() => {
    cleanup();
    mockUpdateFleetSettings.mockReset();
  });

  it("shows disabled federation while keeping saved membership editable", () => {
    renderFleetSettings(fleetSettings());

    const toggle = screen.getByRole("checkbox", {
      name: "Enable fleet federation",
    }) as HTMLInputElement;
    expect(toggle.checked).toBe(false);
    expect(screen.getByText("Remote hosts stay unavailable while federation is off.")).toBeTruthy();
    expect(screen.getByRole("table", { name: "HTTP peer membership" })).toBeTruthy();
    expect(screen.getByRole("table", { name: "SSH peer membership" })).toBeTruthy();
    expect(screen.getByLabelText("HTTP peer mini key")).toBeTruthy();
    expect(screen.getByLabelText("HTTP peer mini base URL")).toBeTruthy();
    expect(screen.getByLabelText("SSH peer epyc key")).toBeTruthy();
    expect(screen.getByLabelText("SSH peer epyc destination")).toBeTruthy();
    expect(screen.getByRole("button", { name: "SSH peer epyc platform: linux" })).toBeTruthy();
  });

  it("saves edited federation settings", async () => {
    const onUpdate = vi.fn();
    const saved = fleetSettings({
      enabled: true,
      key: "hub",
      peer_timeout: "4s",
      restart_required: true,
    });
    mockUpdateFleetSettings.mockReturnValue(Effect.succeed(saved));

    renderFleetSettings(fleetSettings(), onUpdate);

    await fireEvent.click(screen.getByRole("checkbox", { name: "Enable fleet federation" }));
    await fireEvent.input(screen.getByLabelText("Local fleet key"), {
      target: { value: "hub" },
    });
    await fireEvent.input(screen.getByLabelText("Peer timeout"), {
      target: { value: "4s" },
    });
    await fireEvent.input(screen.getByLabelText("HTTP peer mini name"), {
      target: { value: "Mini" },
    });
    await fireEvent.input(screen.getByLabelText("SSH peer epyc remote command"), {
      target: { value: "kenn-forge" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "SSH peer epyc platform: linux" }));
    const platformInput = screen.getByRole("combobox", { name: "SSH peer epyc platform" });
    await fireEvent.input(platformInput, { target: { value: "mac" } });
    await fireEvent.keyDown(platformInput, { key: "ArrowUp" });
    await fireEvent.keyDown(platformInput, { key: "Enter" });
    await fireEvent.click(screen.getByRole("button", { name: "Save fleet federation" }));

    await waitFor(() => {
      expect(mockUpdateFleetSettings).toHaveBeenCalledWith({
        enabled: true,
        key: "hub",
        peer_timeout: "4s",
        sessions: { include_unmanaged_details: false },
        peers: [
          {
            key: "mini",
            name: "Mini",
            base_url: "http://mini.tail:8091",
          },
        ],
        ssh_peers: [
          {
            key: "epyc",
            name: "EPYC",
            destination: "wes@epyc.tail",
            platform: "macos",
            remote_command: "kenn-forge",
          },
        ],
      });
    });
    expect(onUpdate).toHaveBeenCalledWith(saved);
    expect(screen.getByText("Restart required")).toBeTruthy();
  });

  it("keeps SSH platform custom values editable", async () => {
    mockUpdateFleetSettings.mockReturnValue(Effect.succeed(fleetSettings()));

    renderFleetSettings(
      fleetSettings({
        ssh_peers: [
          {
            key: "epyc",
            name: "EPYC",
            destination: "wes@epyc.tail",
            platform: "plan9",
          },
        ],
      }),
    );

    await fireEvent.click(screen.getByRole("button", { name: "SSH peer epyc platform: plan9" }));
    const platformInput = screen.getByRole("combobox", { name: "SSH peer epyc platform" });
    await fireEvent.input(platformInput, { target: { value: "freebsd" } });
    await fireEvent.keyDown(platformInput, { key: "Enter" });
    await fireEvent.click(screen.getByRole("button", { name: "Save fleet federation" }));

    await waitFor(() => {
      expect(mockUpdateFleetSettings).toHaveBeenCalledWith(
        expect.objectContaining({
          ssh_peers: [
            expect.objectContaining({
              platform: "freebsd",
            }),
          ],
        }),
      );
    });
  });

  it("surfaces save errors without discarding the draft", async () => {
    mockUpdateFleetSettings.mockReturnValue(
      Effect.fail({
        _tag: "ApiProblemError",
        operation: "save fleet settings",
        problem: { type: "about:blank", detail: "fleet.peers[0]: base_url is required" },
      }),
    );

    renderFleetSettings(fleetSettings());

    await fireEvent.input(screen.getByLabelText("HTTP peer mini name"), {
      target: { value: "Mini" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save fleet federation" }));

    await waitFor(() =>
      expect(flash.getFlash()).toMatchObject({
        message: "fleet.peers[0]: base_url is required",
        tone: "danger",
      }),
    );
    expect(screen.queryByText("fleet.peers[0]: base_url is required")).toBeNull();
    expect((screen.getByLabelText("HTTP peer mini name") as HTMLInputElement).value).toBe("Mini");
  });
});
