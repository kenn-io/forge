import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import DocsWorkspace from "./DocsWorkspace.svelte";
import { createMockDocsBackend } from "./docsTestBackend";
import { defaultDocsRoute, type DocsRoute } from "../../api/docs/route";
import {
  resetKataDaemonRoster,
  setActiveKataDaemon,
  setKataDaemonRoster,
} from "../../stores/active-kata-daemon.svelte";

afterEach(() => {
  cleanup();
  setActiveKataDaemon(undefined);
  resetKataDaemonRoster();
  // The anchor-from-hash test mutates the location hash; clear it so it
  // can't leak into tests that assume a bare URL.
  if (typeof window !== "undefined") window.location.hash = "";
});

function renderWorkspace(overrides: Partial<DocsRoute> = {}) {
  const route: DocsRoute = { ...defaultDocsRoute, ...overrides };
  const onRouteChange = vi.fn();
  const api = createMockDocsBackend();
  const result = render(DocsWorkspace, { props: { route, onRouteChange, api } });
  return { ...result, onRouteChange };
}

async function openFolderMenu() {
  const trigger = await waitFor(() => {
    const control = screen.getByRole("combobox", { name: /^Switch folder:/ });
    if (control.hasAttribute("disabled")) throw new Error("folder selector still disabled");
    return control;
  });
  await fireEvent.click(trigger);
  return screen.getByRole("listbox");
}

describe("DocsWorkspace", () => {
  test("lists folders and auto-selects the first one", async () => {
    const { onRouteChange } = renderWorkspace();
    const menu = await openFolderMenu();
    expect(within(menu).getByRole("option", { name: "Notes" })).toBeTruthy();
    expect(within(menu).getByRole("option", { name: "Engineering" })).toBeTruthy();
    expect(onRouteChange).toHaveBeenCalledWith(
      expect.objectContaining({ mode: "docs", folder: "notes", doc: null }),
      expect.objectContaining({ replace: true }),
    );
  });

  test("replaces a stale folder route with the first available folder", async () => {
    const { onRouteChange } = renderWorkspace({ folder: "missing", doc: "README.md" });

    await waitFor(() => {
      expect(onRouteChange).toHaveBeenCalledWith({ mode: "docs", folder: "notes", doc: null }, { replace: true });
    });
  });

  test("clears a stale folder route when no folders remain", async () => {
    const route: DocsRoute = { mode: "docs", folder: "missing", doc: "README.md" };
    const onRouteChange = vi.fn();
    const api = createMockDocsBackend({ folders: [] });
    render(DocsWorkspace, { props: { route, onRouteChange, api } });

    await waitFor(() => {
      expect(onRouteChange).toHaveBeenCalledWith({ mode: "docs", folder: null, doc: null }, { replace: true });
    });
  });

  // Tree DOM and filename search are now owned by FolderTree (a thin
  // bridge to @pierre/trees). Those interactions are tested at the
  // unit level (flattenTreePaths) and rely on the library for the
  // DOM; jsdom can't drive its virtualization + DnD reliably. The
  // wrapper-level tests for selection round-trips live alongside the
  // bridge module.

  test("switching folders clears the selected doc", async () => {
    const { onRouteChange } = renderWorkspace({ folder: "notes", doc: "README.md" });
    await waitFor(() => screen.getByText("Notes"));
    onRouteChange.mockClear();
    const menu = await openFolderMenu();
    await fireEvent.click(within(menu).getByRole("option", { name: "Engineering" }));
    expect(onRouteChange).toHaveBeenCalledWith({
      mode: "docs",
      folder: "engineering",
      doc: null,
    });
  });

  test("auto-opens root README.md when landing on a folder with no doc selected", async () => {
    const { onRouteChange } = renderWorkspace({ folder: "notes" });
    await waitFor(() =>
      expect(onRouteChange).toHaveBeenCalledWith(
        expect.objectContaining({ mode: "docs", folder: "notes", doc: "README.md" }),
        expect.objectContaining({ replace: true }),
      ),
    );
  });

  test("renders doc body once a doc is selected", async () => {
    renderWorkspace({ folder: "notes", doc: "README.md" });
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: /Welcome to Notes/ })).toBeTruthy();
    });
  });

  test("route doc identity does not collide when folder ids contain delimiters", async () => {
    const api = createMockDocsBackend({
      folders: [
        {
          meta: { id: "a::b", name: "First", path: "/first" },
          files: { "c.md": "# First Doc\n" },
        },
        {
          meta: { id: "a", name: "Second", path: "/second" },
          files: { "b::c.md": "# Second Doc\n" },
        },
      ],
    });
    const route: DocsRoute = { mode: "docs", folder: "a::b", doc: "c.md" };
    const { rerender } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });

    await waitFor(() => expect(screen.getByRole("heading", { name: "First Doc" })).toBeTruthy());
    await rerender({ route: { mode: "docs", folder: "a", doc: "b::c.md" }, onRouteChange: vi.fn(), api });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Second Doc" })).toBeTruthy());
  });

  test("warns when the active folder daemon binding is stale", async () => {
    setKataDaemonRoster(["home", "work"], "home");
    setActiveKataDaemon("home");
    const api = createMockDocsBackend({
      folders: [
        {
          meta: { id: "archive", name: "Archive", path: "/archive", daemon: "gone" },
          files: { "README.md": "# Archive\n" },
        },
      ],
    });

    render(DocsWorkspace, {
      props: {
        route: { mode: "docs", folder: "archive", doc: "README.md" },
        onRouteChange: vi.fn(),
        api,
      },
    });

    await waitFor(() => screen.getByRole("heading", { name: "Archive" }));
    const warning = screen.getByRole("status");
    expect(warning.textContent).toContain("gone");
    expect(warning.textContent).toContain("active daemon");
  });

  test("does not warn before the daemon roster has resolved", async () => {
    const api = createMockDocsBackend({
      folders: [
        {
          meta: { id: "notes", name: "Notes", path: "/notes", daemon: "work" },
          files: { "README.md": "# Notes\n" },
        },
      ],
    });

    render(DocsWorkspace, {
      props: {
        route: { mode: "docs", folder: "notes", doc: "README.md" },
        onRouteChange: vi.fn(),
        api,
      },
    });

    await waitFor(() => screen.getByRole("heading", { name: "Notes" }));
    expect(screen.queryByRole("status")).toBeNull();
  });

  test("strips YAML frontmatter from the rendered output", async () => {
    renderWorkspace({ folder: "notes", doc: "README.md" });
    await waitFor(() => screen.getByRole("heading", { name: /Welcome to Notes/ }));
    expect(screen.queryByText(/title: Notes/)).toBeNull();
  });

  test("builds a heading outline from the rendered doc", async () => {
    renderWorkspace({ folder: "notes", doc: "Projects/reader.md" });
    const outline = await screen.findByRole("complementary", { name: "Document outline" });
    expect(within(outline).getByRole("button", { name: "Reader" })).toBeTruthy();
    expect(within(outline).getByRole("button", { name: "Architecture" })).toBeTruthy();
  });

  test("scrolls to the heading named in the URL hash on direct navigation", async () => {
    // jsdom has no scrollIntoView; capture the element it would target.
    const proto = window.HTMLElement.prototype as { scrollIntoView?: () => void };
    const original = proto.scrollIntoView;
    const scrolled: string[] = [];
    proto.scrollIntoView = function (this: HTMLElement) {
      scrolled.push(this.id);
    };
    try {
      window.location.hash = "#architecture";
      renderWorkspace({ folder: "notes", doc: "Projects/reader.md" });
      await waitFor(() => expect(screen.getByRole("heading", { name: "Architecture" })).toBeTruthy());
      await waitFor(() => expect(scrolled).toContain("architecture"));
    } finally {
      if (original) proto.scrollIntoView = original;
      else delete proto.scrollIntoView;
    }
  });

  test("does not re-apply a consumed hash anchor to a later document", async () => {
    const proto = window.HTMLElement.prototype as { scrollIntoView?: () => void };
    const original = proto.scrollIntoView;
    const scrolled: string[] = [];
    proto.scrollIntoView = function (this: HTMLElement) {
      scrolled.push(this.id);
    };
    try {
      const api = createMockDocsBackend({
        folders: [
          {
            meta: { id: "notes", name: "Notes", path: "/notes" },
            files: {
              "one.md": "# One\n\n## Architecture\n\nFirst.\n",
              "two.md": "# Two\n\n## Architecture\n\nSecond.\n",
            },
          },
        ],
      });
      window.location.hash = "#architecture";
      const onRouteChange = vi.fn();
      const { rerender } = render(DocsWorkspace, {
        props: { route: { mode: "docs", folder: "notes", doc: "one.md" }, onRouteChange, api },
      });
      await waitFor(() => expect(screen.getByRole("heading", { name: "Architecture" })).toBeTruthy());
      await waitFor(() => expect(scrolled).toContain("architecture"));

      // Navigate to another doc that shares the heading id, the way a
      // folder switch + landing auto-open does (no explicit anchor). The
      // consumed hash anchor must not scroll the new document.
      scrolled.length = 0;
      await rerender({ route: { mode: "docs", folder: "notes", doc: "two.md" }, onRouteChange, api });
      await waitFor(() => expect(screen.getByRole("heading", { name: "Two", level: 1 })).toBeTruthy());
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(scrolled).not.toContain("architecture");
    } finally {
      if (original) proto.scrollIntoView = original;
      else delete proto.scrollIntoView;
    }
  });

  test("clicking a wikilink emits a route change to the resolved doc", async () => {
    const { onRouteChange } = renderWorkspace({ folder: "notes", doc: "README.md" });
    await waitFor(() => screen.getByRole("heading", { name: /Welcome to Notes/ }));
    onRouteChange.mockClear();
    const wikilink = screen.getAllByRole("link").find((el) => el.getAttribute("data-wikilink") === "resolved");
    expect(wikilink).toBeTruthy();
    await fireEvent.click(wikilink!);
    expect(onRouteChange).toHaveBeenCalledWith(
      expect.objectContaining({ mode: "docs", doc: expect.stringMatching(/\.md$/) }),
    );
  });

  test("relative image src is rewritten to the blob URL", async () => {
    renderWorkspace({ folder: "notes", doc: "Projects/reader.md" });
    const img = await waitFor(() => {
      const found = document.querySelector("img[alt='logo']");
      if (!found) throw new Error("logo image not yet rendered");
      return found;
    });
    expect(img.getAttribute("src") ?? "").toMatch(/^data:image\/png/);
  });

  test("clicking Edit swaps the viewer for the editor toolbar", async () => {
    renderWorkspace({ folder: "notes", doc: "README.md" });
    await waitFor(() => expect(screen.getByRole("heading", { name: /Welcome to Notes/ })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(await screen.findByRole("button", { name: "Cancel" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Save/i })).toBeTruthy();
    // The rendered heading should be gone — we're now in the editor.
    expect(screen.queryByRole("heading", { name: /Welcome to Notes/ })).toBeNull();
  });

  test("Cancel restores the viewer when the draft is untouched", async () => {
    renderWorkspace({ folder: "notes", doc: "README.md" });
    await waitFor(() => expect(screen.getByRole("heading", { name: /Welcome to Notes/ })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    await fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Edit" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: /Welcome to Notes/ })).toBeTruthy();
  });

  test("Add folder action opens the AddFolderDialog", async () => {
    renderWorkspace();
    await fireEvent.click(await screen.findByRole("button", { name: /Add folder/ }));
    await waitFor(() => screen.getByRole("dialog", { name: "Add folder" }));
  });

  test("renaming the active folder updates the selector", async () => {
    renderWorkspace({ folder: "notes" });
    await fireEvent.click(await screen.findByRole("button", { name: "Rename Notes" }));
    const dialog = await waitFor(() => screen.getByRole("dialog", { name: "Rename folder" }));
    const input = within(dialog).getByRole("textbox") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "Personal Notes" } });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Rename folder" })).toBeNull());
    expect(screen.getByRole("combobox", { name: "Switch folder: Personal Notes" })).toBeTruthy();
  });

  test("removing the active folder switches to the remaining one", async () => {
    const { onRouteChange } = renderWorkspace({ folder: "notes" });
    await screen.findByRole("combobox", { name: "Switch folder: Notes" });
    onRouteChange.mockClear();
    await fireEvent.click(screen.getByRole("button", { name: "Remove Notes" }));
    const dialog = await waitFor(() => screen.getByRole("dialog", { name: "Remove folder" }));
    await fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() =>
      expect(onRouteChange).toHaveBeenCalledWith({
        mode: "docs",
        folder: "engineering",
        doc: null,
      }),
    );
  });

  test("publish button is hidden for non-git folders", async () => {
    const api = createMockDocsBackend({
      folders: [{ meta: { id: "x", name: "X", path: "/x" }, files: { "README.md": "# x" } }],
    });
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { queryByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    await waitFor(() => expect(queryByRole("button", { name: /publish/i })).toBeNull());
  });

  test("unsafe git config keeps the publish action and surfaces the safety error", async () => {
    const backend = createMockDocsBackend({
      folders: [{ meta: { id: "x", name: "X", path: "/x" }, files: { "README.md": "# x" } }],
    });
    const unsafeError = () => {
      const err = new Error("docs publish refuses repositories with command-bearing git config") as Error & {
        status?: number;
        code?: string;
      };
      err.status = 400;
      err.code = "unsafe_git_config";
      return err;
    };
    const api = {
      ...backend,
      gitStatus: async () => {
        throw unsafeError();
      },
      gitChanges: async () => {
        throw unsafeError();
      },
    };
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { findByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    const button = await findByRole("button", { name: /publish/i });
    await fireEvent.click(button);
    const dialog = await findByRole("dialog", { name: /commit & push docs/i });
    await waitFor(() => expect(within(dialog).getByText(/command-bearing config or attributes/i)).toBeTruthy());
  });

  test("pull button is hidden for non-git folders", async () => {
    const api = createMockDocsBackend({
      folders: [{ meta: { id: "x", name: "X", path: "/x" }, files: { "README.md": "# x" } }],
    });
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { queryByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    await waitFor(() => expect(queryByRole("button", { name: "Pull from git" })).toBeNull());
  });

  test("pull reports the commit and refreshes tree, git status, and the open doc", async () => {
    const backend = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "README.md": "# x" },
          git: { "README.md": "modified" },
        },
      ],
    });
    const tree = vi.fn(backend.tree);
    const gitStatus = vi.fn(backend.gitStatus);
    const readFile = vi.fn(backend.readFile);
    const gitPull = vi.fn(async () => ({
      branch: "main",
      upstream: "origin/main",
      up_to_date: false,
      commit: "abcdef1234567890abcdef1234567890abcdef12",
      short_commit: "abcdef1",
    }));
    const api = { ...backend, tree, gitStatus, readFile, gitPull };
    const route: DocsRoute = { mode: "docs", folder: "x", doc: "README.md" };
    const { findByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    const button = await findByRole("button", { name: "Pull from git" });
    await waitFor(() => expect(readFile).toHaveBeenCalled());
    const treeCalls = tree.mock.calls.length;
    const statusCalls = gitStatus.mock.calls.length;
    const readCalls = readFile.mock.calls.length;
    await fireEvent.click(button);
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Pulled to abcdef1"));
    expect(gitPull).toHaveBeenCalledWith("x");
    expect(tree.mock.calls.length).toBeGreaterThan(treeCalls);
    expect(gitStatus.mock.calls.length).toBeGreaterThan(statusCalls);
    expect(readFile.mock.calls.length).toBeGreaterThan(readCalls);
  });

  test("pull reports an up-to-date repo", async () => {
    const backend = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "README.md": "# x" },
          git: { "README.md": "modified" },
        },
      ],
    });
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { findByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api: backend },
    });
    const button = await findByRole("button", { name: "Pull from git" });
    await fireEvent.click(button);
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Already up to date."));
  });

  test("pull failure surfaces the error in the notice line", async () => {
    const backend = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "README.md": "# x" },
          git: { "README.md": "modified" },
        },
      ],
    });
    const gitPull = vi.fn(async () => {
      const err = new Error("local branch and upstream have diverged; resolve with a git client") as Error & {
        status?: number;
        code?: string;
      };
      err.status = 409;
      err.code = "diverged";
      throw err;
    });
    const api = { ...backend, gitPull };
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { findByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    const button = await findByRole("button", { name: "Pull from git" });
    await fireEvent.click(button);
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("diverged"));
  });

  test("edit is disabled while a pull is in flight so a draft can't capture pre-pull content", async () => {
    const backend = createMockDocsBackend();
    let resolvePull!: (v: {
      branch: string;
      upstream: string;
      up_to_date: boolean;
      commit: string;
      short_commit: string;
    }) => void;
    const gitPull = vi.fn(
      () =>
        new Promise<{
          branch: string;
          upstream: string;
          up_to_date: boolean;
          commit: string;
          short_commit: string;
        }>((resolve) => {
          resolvePull = resolve;
        }),
    );
    const api = { ...backend, gitPull };
    const route: DocsRoute = { mode: "docs", folder: "notes", doc: "README.md" };
    render(DocsWorkspace, { props: { route, onRouteChange: vi.fn(), api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: /Welcome to Notes/ })).toBeTruthy());
    const editButton = screen.getByRole("button", { name: "Edit" });
    expect(editButton.hasAttribute("disabled")).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: "Pull from git" }));
    // Opening the editor mid-pull would capture the pre-pull body and a
    // later save would overwrite the pulled content, so Edit must stay
    // unavailable until the pull settles.
    expect(editButton.hasAttribute("disabled")).toBe(true);
    resolvePull({
      branch: "main",
      upstream: "origin/main",
      up_to_date: true,
      commit: "0000000000000000000000000000000000000000",
      short_commit: "0000000",
    });
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Already up to date."));
    expect(editButton.hasAttribute("disabled")).toBe(false);
  });

  test("switching folders while the pull's refreshes are pending abandons the stale reloads", async () => {
    const backend = createMockDocsBackend();
    let releaseTree!: () => void;
    const treeGate = new Promise<void>((resolve) => {
      releaseTree = resolve;
    });
    let deferNotesTree = false;
    const tree = vi.fn(async (folderID: string) => {
      if (deferNotesTree && folderID === "notes") await treeGate;
      return backend.tree(folderID);
    });
    const gitStatus = vi.fn(backend.gitStatus);
    const readFile = vi.fn(backend.readFile);
    const gitPull = vi.fn(async () => ({
      branch: "main",
      upstream: "origin/main",
      up_to_date: true,
      commit: "0000000000000000000000000000000000000000",
      short_commit: "0000000",
    }));
    const api = { ...backend, tree, gitStatus, readFile, gitPull };
    const onRouteChange = vi.fn();
    const route: DocsRoute = { mode: "docs", folder: "notes", doc: "README.md" };
    const { rerender } = render(DocsWorkspace, { props: { route, onRouteChange, api } });
    await waitFor(() => expect(screen.getByRole("heading", { name: /Welcome to Notes/ })).toBeTruthy());
    const mountTreeCalls = tree.mock.calls.length;
    deferNotesTree = true;
    await fireEvent.click(screen.getByRole("button", { name: "Pull from git" }));
    // The pull resolved and its tree refresh is now parked on the gate.
    await waitFor(() => expect(tree.mock.calls.length).toBeGreaterThan(mountTreeCalls));
    await rerender({ route: { mode: "docs", folder: "engineering", doc: null }, onRouteChange, api });
    await waitFor(() => expect(gitStatus).toHaveBeenCalledWith("engineering"));
    const notesStatusCalls = gitStatus.mock.calls.filter((c) => c[0] === "notes").length;
    const notesReads = readFile.mock.calls.filter((c) => c[0] === "notes").length;
    releaseTree();
    // Let the parked pull refresh chain settle before asserting it went
    // no further than the folder guard.
    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(gitStatus.mock.calls.filter((c) => c[0] === "notes").length).toBe(notesStatusCalls);
    expect(readFile.mock.calls.filter((c) => c[0] === "notes").length).toBe(notesReads);
    // The old folder's pull outcome must not be announced over the new view.
    expect(screen.queryByRole("status")?.textContent ?? "").not.toContain("Already up to date.");
  });

  test("pull button is disabled while the editor is open", async () => {
    renderWorkspace({ folder: "notes", doc: "README.md" });
    await waitFor(() => expect(screen.getByRole("heading", { name: /Welcome to Notes/ })).toBeTruthy());
    const button = screen.getByRole("button", { name: "Pull from git" });
    expect(button.hasAttribute("disabled")).toBe(false);
    await fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    await screen.findByRole("button", { name: "Cancel" });
    expect(button.hasAttribute("disabled")).toBe(true);
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(button.hasAttribute("disabled")).toBe(false));
  });

  test("publish button opens the PublishDocsDialog for a git-backed folder", async () => {
    const api = createMockDocsBackend({
      folders: [
        {
          meta: { id: "x", name: "X", path: "/x" },
          files: { "README.md": "# x" },
          git: { "README.md": "modified" },
        },
      ],
    });
    const route: DocsRoute = { mode: "docs", folder: "x", doc: null };
    const { findByRole, getByRole } = render(DocsWorkspace, {
      props: { route, onRouteChange: vi.fn(), api },
    });
    const button = await findByRole("button", { name: /publish/i });
    await fireEvent.click(button);
    await findByRole("dialog", { name: /commit & push docs/i });
    expect(getByRole("textbox", { name: /commit message/i })).toBeTruthy();
  });
});
