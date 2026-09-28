import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import {
  discardWorkspaceLaunch,
  resetWorkspaceCreatePendingForTest,
} from "../../stores/workspace-create-pending.svelte.js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { STORES_KEY } from "../../context.js";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";

import NewWorkspaceDialog from "./NewWorkspaceDialog.svelte";

const mockGet = vi.fn();
const mockPost = vi.fn();
const mockNavigate = vi.fn();
let preferredTarget = "";
const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));
const launchTargets = [
  {
    key: "codex",
    label: "Codex",
    kind: "agent" as const,
    source: "builtin" as const,
    command: ["codex"],
    available: true,
    disabled_reason: "",
  },
];

vi.mock("../../api/generated/index.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/generated/index.js")>();
  return {
    ...actual,
    KataService: {
      ...actual.KataService,
      listKataDaemons: async () => (await mockGet("/kata/daemons")).data,
    },
  };
});

vi.mock("../../app/runtime.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/runtime.js")>();
  const { makeGeneratedClientFromRouteMocks } = await import("../../testing/test/route-mock-client.js");
  const client = {
    GET: (...args: unknown[]) => mockGet(...args),
    POST: (...args: unknown[]) => mockPost(...args),
  };
  return {
    ...actual,
    makeAppRuntime: () => actual.makeAppRuntime(makeGeneratedClientFromRouteMocks(client)),
  };
});

vi.mock("../../app/runtime-context.js", () => ({
  getAppRuntime: () => {
    const runtime = runtimeCapture.current;
    if (runtime === undefined) throw new Error("new workspace dialog test runtime is not initialized");
    return runtime;
  },
}));

vi.mock("../../stores/router.svelte.js", () => ({
  navigate: (path: string) => mockNavigate(path),
}));

function repoFixture(owner: string, name: string, platformHost = "github.com", platform = "github") {
  return {
    ID: 1,
    Name: name,
    Owner: owner,
    Platform: platform,
    PlatformRepoID: `${platform}-${owner}-${name}`,
    PlatformHost: platformHost,
  };
}

async function renderDialog(props: Record<string, unknown> = {}) {
  const view = render(NewWorkspaceDialog, {
    props: { open: true, onClose: vi.fn(), ...props },
    context: new Map([
      [
        STORES_KEY,
        {
          settings: {
            getWorkspaceSettings: () => ({ default_execution_target: preferredTarget }),
            getLaunchTargets: () => launchTargets,
          },
        },
      ],
    ]),
  });
  await waitFor(() =>
    expect(mockGet).toHaveBeenCalledWith("/repos", expect.objectContaining({ signal: expect.any(AbortSignal) })),
  );
  return view;
}

function repoPicker(): HTMLElement {
  return screen.getByRole("button", { name: /^Filter repositories: / });
}

// The repository picker is kit-ui's Typeahead: the trigger opens the list and
// its option rows commit on mousedown, before focusout can dismiss the panel.
async function pickRepo(label: string): Promise<void> {
  await fireEvent.click(repoPicker());
  await fireEvent.mouseDown(await screen.findByRole("option", { name: new RegExp(label) }));
}

describe("NewWorkspaceDialog", () => {
  beforeEach(() => {
    runtimeCapture.current = makeAppRuntime();
    resetWorkspaceCreatePendingForTest();
    localStorage.clear();
    preferredTarget = "";
    mockGet.mockReset();
    mockPost.mockReset();
    mockNavigate.mockReset();
    mockGet.mockResolvedValue({
      data: [repoFixture("acme", "widget"), repoFixture("acme", "gadget")],
    });
    mockPost.mockResolvedValue({ data: { id: "ws-new" } });
  });

  afterEach(async () => {
    resetWorkspaceCreatePendingForTest();
    cleanup();
    if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
    runtimeCapture.current = undefined;
  });

  it("creates a workspace for the selected repo with no branch when the field is empty", async () => {
    const onClose = vi.fn();
    await renderDialog({ onClose });

    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    const [path, options] = mockPost.mock.calls[0] as [
      string,
      { params: { path: Record<string, string> }; body: Record<string, string> },
    ];
    expect(path).toBe("/repo/{provider}/{owner}/{name}/workspaces");
    expect(options.params.path).toEqual({ provider: "github", owner: "acme", name: "widget" });
    expect(options.body).toEqual({ platform_repo_id: "github-acme-widget" });
    expect(mockNavigate).toHaveBeenCalledWith("/terminal/ws-new");
    expect(onClose).toHaveBeenCalled();
  });

  it("keeps a native submit control so Enter submits the form", async () => {
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));

    const primary = screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement;
    expect(primary.type).toBe("submit");
    const form = screen.getByLabelText("Branch name").closest("form");
    expect(form).not.toBeNull();

    await fireEvent.submit(form!);

    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
  });

  it("creates only from the primary action", async () => {
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalled());
    expect(discardWorkspaceLaunch("ws-new", undefined)).toBeNull();
  });

  it("retains an agent choice through suggested-branch retry", async () => {
    mockPost
      .mockResolvedValueOnce({
        error: {
          code: "branchConflict",
          detail: "branch exists",
          details: {
            branch: "spike/thing",
            suggestedBranch: "spike/thing-2",
          },
        },
      })
      .mockResolvedValueOnce({ data: { id: "ws-second" } });
    await renderDialog();
    await fireEvent.click(
      screen.getByRole("button", {
        name: "Create workspace options",
      }),
    );
    await fireEvent.click(screen.getByRole("menuitem", { name: "Codex" }));
    await fireEvent.click(
      await screen.findByRole("button", {
        name: /Use spike\/thing-2/,
      }),
    );
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(2));
    expect(discardWorkspaceLaunch("ws-second", undefined)).toBe("codex");
  });

  it("sends the typed branch name", async () => {
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));

    await fireEvent.input(screen.getByLabelText("Branch name"), {
      target: { value: "  spike/rate-limits  " },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    const [, options] = mockPost.mock.calls[0] as [string, { body: Record<string, string> }];
    expect(options.body).toEqual({ branch: "spike/rate-limits", platform_repo_id: "github-acme-widget" });
  });

  it("creates against the repo the user picks from the list", async () => {
    await renderDialog();

    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await pickRepo("acme/gadget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    const [, options] = mockPost.mock.calls[0] as [string, { params: { path: Record<string, string> } }];
    expect(options.params.path.name).toBe("gadget");
  });

  it("defaults to the repo work was last started in", async () => {
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await pickRepo("acme/gadget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    cleanup();

    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/gadget"));
  });

  it("restores the last-used repository after a rename and browser reload", async () => {
    await renderDialog();
    await pickRepo("acme/gadget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith("/terminal/ws-new"));
    cleanup();
    await Effect.runPromise(runtimeCapture.current!.disposeEffect);
    runtimeCapture.current = makeAppRuntime();
    mockGet.mockResolvedValue({
      data: [
        repoFixture("acme", "widget"),
        { ...repoFixture("acme", "gadget-next"), PlatformRepoID: "github-acme-gadget" },
      ],
    });

    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/gadget-next"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(2));
    expect(mockPost).toHaveBeenLastCalledWith(
      "/repo/{provider}/{owner}/{name}/workspaces",
      expect.objectContaining({
        params: { path: { provider: "github", owner: "acme", name: "gadget-next" } },
        body: { platform_repo_id: "github-acme-gadget" },
      }),
    );
  });

  it("requires a new choice when the last-used repository route is reused after a reload", async () => {
    await renderDialog();
    await pickRepo("acme/gadget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith("/terminal/ws-new"));
    cleanup();
    await Effect.runPromise(runtimeCapture.current!.disposeEffect);
    runtimeCapture.current = makeAppRuntime();
    mockGet.mockResolvedValue({
      data: [repoFixture("acme", "widget"), { ...repoFixture("acme", "gadget"), PlatformRepoID: "replacement-id" }],
    });

    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).not.toContain("Loading repositories"));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
    expect(repoPicker().textContent).not.toContain("acme/gadget");
    expect(repoPicker().textContent).not.toContain("acme/widget");
    await fireEvent.submit(screen.getByLabelText("Branch name").closest("form")!);
    expect(mockPost).toHaveBeenCalledTimes(1);

    await pickRepo("acme/gadget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(2));
    expect(mockPost).toHaveBeenLastCalledWith(
      "/repo/{provider}/{owner}/{name}/workspaces",
      expect.objectContaining({ body: { platform_repo_id: "replacement-id" } }),
    );
  });

  it("prefers the seeded repo over the last used one", async () => {
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await pickRepo("acme/gadget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    cleanup();

    await renderDialog({
      seedRepo: {
        provider: "github",
        platformHost: "github.com",
        platformRepoId: "github-acme-widget",
        owner: "acme",
        name: "widget",
      },
    });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
  });

  it("requires a new choice for a saved preference without a stable repository ID", async () => {
    localStorage.setItem("kenn-forge:workspace:new_repo", "github/github.com/acme/widget");
    await renderDialog();

    await waitFor(() => expect(repoPicker().textContent).not.toContain("Loading repositories"));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
    expect(repoPicker().textContent).not.toContain("acme/widget");
    await pickRepo("acme/widget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
  });

  it("preselects the seeded repo", async () => {
    await renderDialog({
      seedRepo: {
        provider: "github",
        platformHost: "github.com",
        platformRepoId: "github-acme-gadget",
        owner: "acme",
        name: "gadget",
      },
    });

    await waitFor(() => expect(repoPicker().textContent).toContain("acme/gadget"));
  });

  it("requires an explicit choice when the seeded repo is not offered", async () => {
    // A seed that no longer resolves (for example a repository hidden from
    // the UI) must not silently divert the workspace to another repository,
    // even when a last-used repo is remembered.
    localStorage.setItem("kenn-forge:workspace:new_repo", "github|github.com|id|github-acme-gadget");
    await renderDialog({
      seedRepo: {
        provider: "github",
        platformHost: "github.com",
        platformRepoId: "github-acme-concealed",
        owner: "acme",
        name: "concealed",
      },
    });

    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true),
    );
    expect(repoPicker().textContent).not.toContain("acme/widget");
    expect(repoPicker().textContent).not.toContain("acme/gadget");
  });

  it("does not preselect a replacement for the repository of the current workspace", async () => {
    await renderDialog({
      seedRepo: {
        provider: "github",
        platformHost: "github.com",
        platformRepoId: "original-id",
        owner: "acme",
        name: "widget",
      },
    });
    await waitFor(() => expect(repoPicker().textContent).not.toContain("Loading repositories"));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
    expect(repoPicker().textContent).not.toContain("acme/widget");
  });

  it.each(["", "github|github.com|id|github-acme-gadget"])(
    "requires a choice for an inactive workspace seed without a repository ID (saved: %s)",
    async (lastUsed) => {
      if (lastUsed) localStorage.setItem("kenn-forge:workspace:new_repo", lastUsed);
      mockGet.mockResolvedValue({
        data: [{ ...repoFixture("acme", "widget"), PlatformRepoID: "replacement-id" }, repoFixture("acme", "gadget")],
      });
      // An inactive repository's workspace keeps its route in the sidebar,
      // but its snapshot no longer includes the repository's stable ID.
      await renderDialog({
        seedRepo: { provider: "github", platformHost: "github.com", owner: "acme", name: "widget" },
      });

      await waitFor(() => expect(repoPicker().textContent).not.toContain("Loading repositories"));
      expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
      expect(repoPicker().textContent).not.toContain("acme/widget");
      expect(repoPicker().textContent).not.toContain("acme/gadget");
      await fireEvent.submit(screen.getByLabelText("Branch name").closest("form")!);
      expect(mockPost).not.toHaveBeenCalled();

      await pickRepo("acme/widget");
      await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
      await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
      expect(mockPost).toHaveBeenCalledWith(
        "/repo/{provider}/{owner}/{name}/workspaces",
        expect.objectContaining({
          params: { path: { provider: "github", owner: "acme", name: "widget" } },
          body: { platform_repo_id: "replacement-id" },
        }),
      );
    },
  );

  it("keeps requiring a choice for an unavailable seed after switching sources", async () => {
    // The seed promise holds across source switches: leaving for Kata and
    // returning to the repository source must not divert to the last-used
    // or first repository when the seed is still unavailable.
    localStorage.setItem("kenn-forge:workspace:new_repo", "github|github.com|id|github-acme-gadget");
    await renderDialog({
      seedRepo: {
        provider: "github",
        platformHost: "github.com",
        platformRepoId: "github-acme-concealed",
        owner: "acme",
        name: "concealed",
      },
    });
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true),
    );

    await fireEvent.click(screen.getByRole("button", { name: "Kata issue" }));
    await fireEvent.click(screen.getByRole("button", { name: "Repository" }));
    await waitFor(() => expect(mockGet.mock.calls.filter((c) => c[0] === "/repos")).toHaveLength(2));

    // Once the reload settles, a wrong automatic selection would surface in
    // the picker trigger and enable submission. The trigger renders as a
    // button while closed and a combobox while open, so accept either.
    await waitFor(() => {
      const trigger =
        screen.queryByRole("button", { name: /^Filter repositories: / }) ??
        screen.getByRole("combobox", { name: "Filter repositories" });
      expect(trigger.textContent).not.toContain("Loading repositories");
      expect(trigger.textContent).not.toContain("acme/gadget");
      expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
    });
  });

  it("accepts the current Kata API schema", async () => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/repos") return Promise.resolve({ data: [repoFixture("acme", "widget")] });
      if (path === "/kata/daemons") {
        return Promise.resolve({
          data: {
            daemons: [
              {
                id: "current",
                url: "http://current",
                health: "connected",
                auth: "none",
                default: true,
                api_schema_version: "0.13.0",
              },
            ],
          },
        });
      }
      throw new Error(`unexpected GET ${path}`);
    });
    await renderDialog();

    await fireEvent.click(screen.getByRole("button", { name: "Kata issue" }));
    const search = (await screen.findByRole("searchbox", { name: "Search Kata issues" })) as HTMLInputElement;
    await waitFor(() => expect(search.disabled).toBe(false));
  });

  it("routes non-default hosts through the host-scoped path", async () => {
    mockGet.mockResolvedValue({ data: [repoFixture("acme", "widget", "git.example.test", "forgejo")] });
    await renderDialog();

    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    const [path, options] = mockPost.mock.calls[0] as [string, { params: { path: Record<string, string> } }];
    expect(path).toBe("/host/{platform_host}/repo/{provider}/{owner}/{name}/workspaces");
    expect(options.params.path.platform_host).toBe("git.example.test");
  });

  it("creates and opens repository workspaces on the selected spoke", async () => {
    mockGet.mockImplementation((path: string) => {
      if (path === "/repos") return Promise.resolve({ data: [repoFixture("acme", "widget")] });
      if (path === "/snapshot") {
        return Promise.resolve({
          data: {
            hosts: [
              {
                configKey: "hub-node",
                federationRole: "hub",
                kind: "self",
                name: "Studio hub",
                operationAvailability: { workspaceWrite: { available: true } },
              },
              {
                configKey: "build-node",
                federationRole: "spoke",
                kind: "remote",
                name: "Build spoke",
                operationAvailability: { workspaceWrite: { available: true } },
              },
            ],
          },
        });
      }
      throw new Error(`unexpected GET ${path}`);
    });
    await renderDialog();

    const machine = await screen.findByRole("combobox", {
      name: "Workspace machine: Studio hub (this machine)",
    });
    await fireEvent.click(machine);
    await fireEvent.click(screen.getByRole("option", { name: "Build spoke" }));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    const [path, options] = mockPost.mock.calls[0] as [
      string,
      { params: { path: Record<string, string> }; body: Record<string, string> },
    ];
    expect(path).toBe("/fleet/hosts/{host_key}/repo/{provider}/{owner}/{name}/workspaces");
    expect(options.params.path).toEqual({
      host_key: "build-node",
      provider: "github",
      owner: "acme",
      name: "widget",
    });
    expect(options.body).toEqual({ platform_repo_id: "github-acme-widget" });
    expect(mockNavigate).toHaveBeenCalledWith("/terminal/fleet/build-node/ws-new");
  });

  it("uses the preferred devbox and keeps its target separate from fleet peers", async () => {
    preferredTarget = "devbox:compute-a";
    mockGet.mockImplementation((path: string) =>
      Promise.resolve({
        data:
          path === "/snapshot"
            ? {
                hosts: [
                  {
                    configKey: "hub",
                    federationRole: "hub",
                    kind: "self",
                    name: "Hub",
                    operationAvailability: { workspaceWrite: { available: true } },
                  },
                  {
                    configKey: "devbox:compute-a",
                    federationRole: "devbox",
                    kind: "devbox",
                    name: "Compute A",
                    operationAvailability: { workspaceWrite: { available: true } },
                  },
                ],
              }
            : [repoFixture("acme", "widget")],
      }),
    );
    await renderDialog();
    await screen.findByRole("combobox", { name: /Workspace machine: Compute A \(devbox\)/ });
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    expect(mockPost.mock.calls[0][0]).toBe("/devboxes/{connection_id}/workspaces");
    expect(mockPost.mock.calls[0][1]).toMatchObject({
      params: { path: { connection_id: "compute-a" } },
      body: { provider: "github", owner: "acme", name: "widget", platform_repo_id: "github-acme-widget" },
    });
    expect(mockNavigate).toHaveBeenCalledWith("/terminal/fleet/devbox%3Acompute-a/ws-new");
  });

  it("submits the preferred devbox before fleet discovery and retains its submitted destination", async () => {
    preferredTarget = "devbox:compute-a";
    const snapshot = Promise.withResolvers<unknown>();
    const created = Promise.withResolvers<unknown>();
    mockGet.mockImplementation((path: string) =>
      path === "/snapshot" ? snapshot.promise : Promise.resolve({ data: [repoFixture("acme", "widget")] }),
    );
    mockPost.mockReturnValue(created.promise);
    const onCreated = vi.fn();
    const view = await renderDialog({ onCreated });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    expect(screen.getByRole("combobox", { name: /Workspace machine:.*checking/i })).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace options" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Codex" }));
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/devboxes/{connection_id}/workspaces",
        expect.objectContaining({
          params: { path: { connection_id: "compute-a" } },
          body: {
            provider: "github",
            platform_host: "github.com",
            owner: "acme",
            name: "widget",
            platform_repo_id: "github-acme-widget",
          },
        }),
      ),
    );
    await view.rerender({ open: false });
    preferredTarget = "";
    await view.rerender({ open: true });
    created.resolve({ data: { id: "ws-devbox" } });
    await waitFor(() => expect(discardWorkspaceLaunch("ws-devbox", "devbox:compute-a")).toBe("codex"));
    expect(onCreated).not.toHaveBeenCalled();
    snapshot.resolve({ data: { hosts: [] } });
  });

  it("keeps an explicit local choice when the delayed preferred-machine lookup completes", async () => {
    preferredTarget = "devbox:compute-a";
    const snapshot = Promise.withResolvers<unknown>();
    mockGet.mockImplementation((path: string) =>
      path === "/snapshot" ? snapshot.promise : Promise.resolve({ data: [repoFixture("acme", "widget")] }),
    );
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("combobox", { name: /Workspace machine:/ }));
    await fireEvent.click(screen.getByRole("option", { name: "This Forge machine" }));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(false);
    snapshot.resolve({
      data: {
        hosts: [
          {
            configKey: "hub",
            kind: "self",
            federationRole: "hub",
            name: "Studio",
            operationAvailability: { workspaceWrite: { available: true } },
          },
          {
            configKey: "devbox:compute-a",
            kind: "devbox",
            name: "Compute A",
            operationAvailability: { workspaceWrite: { available: true } },
          },
        ],
      },
    });
    await screen.findByRole("combobox", { name: "Workspace machine: Studio (this machine)" });
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/repo/{provider}/{owner}/{name}/workspaces",
        expect.objectContaining({ params: { path: { provider: "github", owner: "acme", name: "widget" } } }),
      ),
    );
  });

  it("restores cached unavailable machines while refreshing and blocks a freshly removed choice", async () => {
    const snapshot = Promise.withResolvers<unknown>();
    let refreshing = false;
    mockGet.mockImplementation((path: string) =>
      path === "/snapshot"
        ? refreshing
          ? snapshot.promise
          : Promise.resolve({
              data: {
                hosts: [
                  {
                    configKey: "hub",
                    kind: "self",
                    federationRole: "hub",
                    name: "Studio",
                    operationAvailability: { workspaceWrite: { available: true } },
                  },
                  {
                    configKey: "devbox:compute-a",
                    kind: "devbox",
                    name: "Compute A",
                    operationAvailability: {
                      workspaceWrite: { available: false, unavailableReason: "Devbox is in maintenance" },
                    },
                  },
                ],
              },
            })
        : Promise.resolve({ data: [repoFixture("acme", "widget")] }),
    );
    const view = await renderDialog();
    await screen.findByRole("combobox", { name: "Workspace machine: Studio (this machine)" });
    view.unmount();
    refreshing = true;
    await renderDialog();
    await fireEvent.click(await screen.findByRole("combobox", { name: "Workspace machine: Studio (this machine)" }));
    await fireEvent.click(screen.getByRole("option", { name: "Compute A (devbox)" }));
    await screen.findByRole("combobox", { name: "Workspace machine: Compute A (devbox)" });
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(false);
    snapshot.resolve({
      data: {
        hosts: [
          {
            configKey: "hub",
            kind: "self",
            federationRole: "hub",
            name: "Studio",
            operationAvailability: { workspaceWrite: { available: true } },
          },
        ],
      },
    });
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true),
    );
    await fireEvent.submit(screen.getByLabelText("Branch name").closest("form")!);
    expect(mockPost).not.toHaveBeenCalled();
  });

  it("blocks an unsupported repository on the preferred devbox before discovery completes", async () => {
    preferredTarget = "devbox:compute-a";
    const snapshot = Promise.withResolvers<unknown>();
    mockGet.mockImplementation((path: string) =>
      path === "/snapshot"
        ? snapshot.promise
        : Promise.resolve({ data: [repoFixture("acme", "widget", "gitlab.com", "gitlab")] }),
    );
    await renderDialog();
    await screen.findByText(/Devboxes currently support only github.com/);
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.submit(screen.getByLabelText("Branch name").closest("form")!);
    expect(mockPost).not.toHaveBeenCalled();
    snapshot.resolve({ data: { hosts: [] } });
  });

  it("uses a newly saved devbox absent from cached hosts while refresh is pending or fails", async () => {
    const snapshot = Promise.withResolvers<unknown>();
    let refreshing = false;
    mockGet.mockImplementation((path: string) =>
      path === "/snapshot"
        ? refreshing
          ? snapshot.promise
          : Promise.resolve({ data: { hosts: [] } })
        : Promise.resolve({ data: [repoFixture("acme", "widget")] }),
    );
    const view = await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    view.unmount();
    preferredTarget = "devbox:added";
    refreshing = true;
    await renderDialog();
    await screen.findByRole("combobox", { name: /Workspace machine:.*checking/i });
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(false);
    snapshot.reject(new Error("snapshot unavailable"));
    await screen.findByRole("combobox", { name: /Workspace machine:.*status unavailable/i });
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/devboxes/{connection_id}/workspaces",
        expect.objectContaining({ params: { path: { connection_id: "added" } } }),
      ),
    );
  });

  it.each([
    ["github", "github.com", false, "Devbox is in maintenance"],
    ["github", "github.example.com", true, "Devboxes currently support only github.com"],
    ["gitlab", "gitlab.com", true, "Devboxes currently support only github.com"],
    ["forgejo", "code.example.com", true, "Devboxes currently support only github.com"],
    ["gitea", "git.example.com", true, "Devboxes currently support only github.com"],
  ])("blocks an unavailable devbox for %s at %s (workspaceWrite=%s)", async (provider, host, available, reason) => {
    preferredTarget = "devbox:compute-a";
    mockGet.mockImplementation((path: string) =>
      Promise.resolve({
        data:
          path === "/snapshot"
            ? {
                hosts: [
                  {
                    configKey: "local",
                    federationRole: "hub",
                    kind: "self",
                    name: "Laptop",
                    operationAvailability: { workspaceWrite: { available: true } },
                  },
                  {
                    configKey: preferredTarget,
                    federationRole: "devbox",
                    kind: "devbox",
                    name: "Compute A",
                    operationAvailability: { workspaceWrite: { available, unavailableReason: reason } },
                  },
                ],
              }
            : [repoFixture("acme", "widget", host, provider)],
      }),
    );
    await renderDialog();
    await screen.findByRole("combobox", { name: /Workspace machine: Compute A \(devbox\)/ });
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true),
    );
    expect(screen.getByText(new RegExp(reason))).toBeTruthy();
    // Enter/form submission must respect the same gate as the split button.
    await fireEvent.submit(screen.getByLabelText("Branch name").closest("form")!);
    expect(mockPost).not.toHaveBeenCalled();
  });

  it("does not create locally when the preferred devbox is unavailable", async () => {
    preferredTarget = "devbox:missing";
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Your selected machine is unavailable/)).toBeTruthy();
    expect(mockPost).not.toHaveBeenCalled();
  });

  it("offers the suggested branch when the requested one already exists", async () => {
    mockPost.mockResolvedValue({
      error: {
        code: "branchConflict",
        detail: "A local branch with the requested name already exists.",
        details: { branch: "spike/thing", suggestedBranch: "spike/thing-2" },
      },
    });
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));

    await fireEvent.input(screen.getByLabelText("Branch name"), {
      target: { value: "spike/thing" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    const suggestion = await screen.findByRole("button", { name: /Use spike\/thing-2/ });
    expect(mockNavigate).not.toHaveBeenCalled();

    mockPost.mockResolvedValue({ data: { id: "ws-second" } });
    await fireEvent.click(suggestion);
    expect((screen.getByLabelText("Branch name") as HTMLInputElement).value).toBe("spike/thing-2");

    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(2));
    const [, options] = mockPost.mock.calls[1] as [string, { body: Record<string, string> }];
    expect(options.body).toEqual({ branch: "spike/thing-2", platform_repo_id: "github-acme-widget" });
  });

  it("keeps the dialog open and reports the failure when the create fails", async () => {
    const onClose = vi.fn();
    mockPost.mockResolvedValue({ error: { detail: "repository not tracked" } });
    await renderDialog({ onClose });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));

    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    expect((await screen.findByRole("alert")).textContent).toContain("repository not tracked");
    expect(onClose).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("abandons the navigation when the dialog is dismissed mid-create", async () => {
    let resolvePost: (value: unknown) => void = () => {};
    mockPost.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvePost = resolve;
        }),
    );
    const { rerender } = await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));

    // Escape and backdrop clicks close the shared modal even while a create is
    // in flight, so the created workspace must not yank the user away.
    await rerender({ open: false });
    resolvePost({ data: { id: "ws-new" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("publishes an agent choice from a stale dialog session without affecting the fresh session", async () => {
    let resolveStale: (value: unknown) => void = () => {};
    mockPost.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveStale = resolve;
        }),
    );
    const onCreated = vi.fn();
    const { rerender } = await renderDialog({ onCreated });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace options" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Codex" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));

    await rerender({ open: false, onCreated });
    await rerender({ open: true, onCreated });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));

    resolveStale({ data: { id: "ws-stale" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(mockNavigate).not.toHaveBeenCalled();
    expect(onCreated).not.toHaveBeenCalled();
    expect(discardWorkspaceLaunch("ws-stale", undefined)).toBe("codex");

    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith("ws-new", undefined));
    expect(discardWorkspaceLaunch("ws-new", undefined)).toBeNull();
  });

  it("keeps the form locked when a stale create resolves under a newer one", async () => {
    const resolvers: ((value: unknown) => void)[] = [];
    mockPost.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvers.push(resolve);
        }),
    );
    const { rerender } = await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));

    // Dismiss mid-create, reopen, and start a second create; the first request
    // is still outstanding.
    await rerender({ open: false });
    await rerender({ open: true });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(2));

    resolvers[0]?.({ data: { id: "ws-stale" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    // The second create still owns the dialog, so the form stays locked (the
    // submit button still reads "Creating…") and no third request can fire.
    const submit = screen.getByRole("button", { name: "Creating…" }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    await fireEvent.click(submit);
    expect(mockPost).toHaveBeenCalledTimes(2);
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("restores repository choices on reopen and preserves a new selection during refresh", async () => {
    const { rerender } = await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await rerender({ open: false });

    const response = Promise.withResolvers<unknown>();
    mockGet.mockImplementation((path: string) =>
      path === "/repos" ? response.promise : Promise.resolve({ data: { hosts: [] } }),
    );
    await rerender({ open: true });

    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await pickRepo("acme/gadget");

    response.resolve({ data: [repoFixture("acme", "new-repo"), repoFixture("acme", "gadget")] });
    await fireEvent.click(repoPicker());
    await screen.findByRole("option", { name: /acme\/new-repo/ });
    await fireEvent.keyDown(screen.getByRole("combobox", { name: "Filter repositories" }), { key: "Escape" });
    expect(repoPicker().textContent).toContain("acme/gadget");
    expect(mockPost).not.toHaveBeenCalled();
  });

  it("clears a cached selection when the same route belongs to a different repository", async () => {
    const { rerender } = await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await rerender({ open: false });
    const response = Promise.withResolvers<unknown>();
    mockGet.mockImplementation((path: string) =>
      path === "/repos" ? response.promise : Promise.resolve({ data: { hosts: [] } }),
    );
    await rerender({ open: true });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));

    response.resolve({ data: [{ ...repoFixture("acme", "widget"), PlatformRepoID: "replacement-id" }] });
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true),
    );
    expect(repoPicker().textContent).not.toContain("acme/widget");
    expect(mockPost).not.toHaveBeenCalled();

    await pickRepo("acme/widget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/repo/{provider}/{owner}/{name}/workspaces",
        expect.objectContaining({ body: { platform_repo_id: "replacement-id" } }),
      ),
    );
  });

  it("submits the cached repository ID while catalog refresh is pending", async () => {
    const { rerender } = await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await rerender({ open: false });
    mockGet.mockImplementation((path: string) =>
      path === "/repos" ? new Promise(() => {}) : Promise.resolve({ data: { hosts: [] } }),
    );
    await rerender({ open: true });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() =>
      expect(mockPost).toHaveBeenCalledWith(
        "/repo/{provider}/{owner}/{name}/workspaces",
        expect.objectContaining({ body: { platform_repo_id: "github-acme-widget" } }),
      ),
    );
  });

  it("interrupts the repository load when the dialog closes", async () => {
    let repositoryLoadInterrupted = false;
    mockGet.mockImplementation(
      (_path: string, options?: { signal?: AbortSignal }) =>
        new Promise(() => {
          options?.signal?.addEventListener("abort", () => {
            repositoryLoadInterrupted = true;
          });
        }),
    );

    const { rerender } = await renderDialog();
    await rerender({ open: false });

    expect(repositoryLoadInterrupted).toBe(true);
  });

  it("keeps cached repositories selectable when a background refresh fails", async () => {
    const { rerender } = await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await rerender({ open: false });
    mockGet.mockRejectedValue(new Error("network down"));
    await rerender({ open: true });

    await fireEvent.click(repoPicker());
    await screen.findByRole("alert");
    await fireEvent.mouseDown(screen.getByRole("option", { name: /acme\/gadget/ }));
    expect(repoPicker().textContent).toContain("acme/gadget");
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("reports a rejected create instead of failing silently", async () => {
    mockPost.mockRejectedValue(new Error("network down"));
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));

    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));

    expect((await screen.findByRole("alert")).textContent).toContain("Could not create workspace");
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("stops loading and reports when the repository request is rejected", async () => {
    mockGet.mockRejectedValue(new Error("network down"));
    await renderDialog();

    await waitFor(() => expect(repoPicker().textContent).toContain("No tracked repositories yet"));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("cannot submit when the repository list fails to load", async () => {
    mockGet.mockResolvedValue({ error: { detail: "repositories unavailable" } });
    await renderDialog();

    await waitFor(() => expect(repoPicker().textContent).toContain("No tracked repositories yet"));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("cannot submit when no repository is tracked", async () => {
    mockGet.mockResolvedValue({ data: [] });
    await renderDialog();

    await waitFor(() => expect(repoPicker().textContent).toContain("No tracked repositories yet"));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
