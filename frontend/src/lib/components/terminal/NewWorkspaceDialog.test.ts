import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import NewWorkspaceDialog from "./NewWorkspaceDialog.svelte";

const mockGet = vi.fn();
const mockPost = vi.fn();
const mockNavigate = vi.fn();

vi.mock("../../api/runtime.js", () => ({
  apiErrorMessage: (error: { detail?: string; title?: string } | undefined, fallback: string) =>
    error?.detail ?? error?.title ?? fallback,
  client: {
    GET: (...args: unknown[]) => mockGet(...args),
    POST: (...args: unknown[]) => mockPost(...args),
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
    PlatformHost: platformHost,
  };
}

async function renderDialog(props: Record<string, unknown> = {}) {
  const view = render(NewWorkspaceDialog, {
    props: { open: true, onClose: vi.fn(), ...props },
  });
  await waitFor(() => expect(mockGet).toHaveBeenCalledWith("/repos"));
  return view;
}

function repoPicker(): HTMLElement {
  return screen.getByRole("button", { name: "Filter repositories" });
}

// The repository picker is kit-ui's Typeahead: the trigger opens the list and
// its option rows commit on mousedown, before focusout can dismiss the panel.
async function pickRepo(label: string): Promise<void> {
  await fireEvent.click(repoPicker());
  await fireEvent.mouseDown(await screen.findByRole("option", { name: new RegExp(label) }));
}

describe("NewWorkspaceDialog", () => {
  beforeEach(() => {
    localStorage.clear();
    mockGet.mockReset();
    mockPost.mockReset();
    mockNavigate.mockReset();
    mockGet.mockResolvedValue({
      data: [repoFixture("acme", "widget"), repoFixture("acme", "gadget")],
    });
    mockPost.mockResolvedValue({ data: { id: "ws-new" } });
  });

  afterEach(() => {
    cleanup();
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
    expect(options.body).toEqual({});
    expect(mockNavigate).toHaveBeenCalledWith("/terminal/ws-new");
    expect(onClose).toHaveBeenCalled();
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
    expect(options.body).toEqual({ branch: "spike/rate-limits" });
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

  it("prefers the seeded repo over the last used one", async () => {
    await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await pickRepo("acme/gadget");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(mockPost).toHaveBeenCalledTimes(1));
    cleanup();

    await renderDialog({
      seedRepo: { provider: "github", platformHost: "github.com", owner: "acme", name: "widget" },
    });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
  });

  it("falls back to the first repo when the last used one is no longer tracked", async () => {
    localStorage.setItem("middleman:workspace:new_repo", "github/github.com/acme/retired");
    await renderDialog();

    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
  });

  it("preselects the seeded repo", async () => {
    await renderDialog({
      seedRepo: { provider: "github", platformHost: "github.com", owner: "acme", name: "gadget" },
    });

    await waitFor(() => expect(repoPicker().textContent).toContain("acme/gadget"));
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

  it("offers the suggested branch when the requested one already exists", async () => {
    mockPost.mockResolvedValue({
      error: {
        code: "branchConflict",
        detail: "A local branch with the requested name already exists.",
        errors: [
          { location: "body.branch", value: "spike/thing" },
          { location: "body.suggested_branch", value: "spike/thing-2" },
        ],
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
    expect(options.body).toEqual({ branch: "spike/thing-2" });
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

  it("cannot submit against the previous selection while a reopen reloads", async () => {
    const { rerender } = await renderDialog();
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/widget"));
    await rerender({ open: false });

    let resolveGet: (value: unknown) => void = () => {};
    mockGet.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveGet = resolve;
        }),
    );
    await rerender({ open: true });

    await waitFor(() => expect(repoPicker().textContent).toContain("Loading repositories"));
    expect((screen.getByRole("button", { name: "Create workspace" }) as HTMLButtonElement).disabled).toBe(true);

    resolveGet({ data: [repoFixture("acme", "gadget")] });
    await waitFor(() => expect(repoPicker().textContent).toContain("acme/gadget"));
    expect(mockPost).not.toHaveBeenCalled();
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
