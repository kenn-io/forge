import { cleanup, render, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { OwnedAppRuntime } from "../../app/runtime.js";
import { navigate } from "../../stores/router.svelte.js";
import { getInlineWorkspaceController, resetWorkspaceHostForTest } from "../../stores/workspace-host.svelte.js";

const mocks = vi.hoisted(() => ({
  runtimeClient: {
    GET: vi.fn(),
    POST: vi.fn(),
    PUT: vi.fn(),
    DELETE: vi.fn(),
  },
}));
const runtimeState = vi.hoisted<{ appRuntime?: OwnedAppRuntime }>(() => ({}));

vi.mock("../../api/runtime.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/runtime.js")>();
  return {
    ...actual,
    client: mocks.runtimeClient,
    createRuntimeClient: () => mocks.runtimeClient,
  };
});

vi.mock("../../app/runtime-context.js", async () => {
  const { makeAppRuntime } = await import("../../app/runtime.js");
  runtimeState.appRuntime = makeAppRuntime();
  return { getAppRuntime: () => runtimeState.appRuntime };
});

vi.mock("../../context.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../context.js")>();
  return {
    ...actual,
    getStores: () => ({
      settings: {
        getTerminalSettings: () => ({
          font_family: "",
          font_size: 14,
          scrollback: 1000,
          line_height: 1,
          letter_spacing: 0,
          cursor_blink: true,
          font_ligatures: false,
        }),
      },
    }),
  };
});

import MobileWorkspaceTerminal from "./MobileWorkspaceTerminal.svelte";

const identity = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widgets",
  repoPath: "acme/widgets",
  number: 7,
  itemType: "pull" as const,
};
const workspaceRef = { id: "ws-a", status: "ready" };

describe("MobileWorkspaceTerminal", () => {
  afterAll(async () => {
    if (runtimeState.appRuntime !== undefined) {
      await Effect.runPromise(runtimeState.appRuntime.disposeEffect);
    }
  });

  beforeEach(() => {
    mocks.runtimeClient.GET.mockReset();
    mocks.runtimeClient.POST.mockReset();
    mocks.runtimeClient.PUT.mockReset();
    mocks.runtimeClient.DELETE.mockReset();
    localStorage.clear();
    resetWorkspaceHostForTest();
    navigate("/pulls");
  });

  afterEach(() => {
    cleanup();
    resetWorkspaceHostForTest();
  });

  it("returns to the workspace list without treating a lookup miss as deletion", async () => {
    mocks.runtimeClient.GET.mockResolvedValue({
      error: {
        type: "about:blank",
        title: "Not found",
        status: 404,
        detail: "workspace not found",
        code: "workspaceNotFound",
      },
      response: new Response(null, { status: 404 }),
    });
    const controller = getInlineWorkspaceController("prs");
    controller.claim(identity, workspaceRef);
    const onMissing = vi.fn();

    render(MobileWorkspaceTerminal, {
      props: {
        workspaceId: "ws-a",
        visible: false,
        onBack: vi.fn(),
        onMissing,
        onOpenItem: vi.fn(),
      },
    });

    await waitFor(() => expect(onMissing).toHaveBeenCalledOnce());
    expect(controller.effectiveWorkspaceRef(identity, workspaceRef)).toEqual(workspaceRef);
  });
});
