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
    const btn = screen.getByRole("button", { name: "Switch folder" });
    if (btn.hasAttribute("disabled")) throw new Error("folder chip still disabled");
    return btn;
  });
  await fireEvent.click(trigger);
  return screen.getByRole("listbox", { name: "Folders" });
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

  test("Add folder entry opens the AddFolderDialog", async () => {
    renderWorkspace();
    const menu = await openFolderMenu();
    await fireEvent.click(within(menu).getByRole("button", { name: /Add folder/ }));
    await waitFor(() => screen.getByRole("dialog", { name: "Add folder" }));
  });

  test("renaming a folder from the menu updates the list", async () => {
    const { onRouteChange } = renderWorkspace();
    await waitFor(() => screen.getByRole("button", { name: "Switch folder" }));
    onRouteChange.mockClear();
    const menu = await openFolderMenu();
    await fireEvent.click(within(menu).getByRole("button", { name: "Rename Notes" }));
    const dialog = await waitFor(() => screen.getByRole("dialog", { name: "Rename folder" }));
    const input = within(dialog).getByRole("textbox") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "Personal Notes" } });
    await fireEvent.click(within(dialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Rename folder" })).toBeNull());
    const reopened = await openFolderMenu();
    expect(within(reopened).getByRole("option", { name: "Personal Notes" })).toBeTruthy();
  });

  test("removing the active folder switches to the remaining one", async () => {
    const { onRouteChange } = renderWorkspace({ folder: "notes" });
    await waitFor(() => screen.getByRole("button", { name: "Switch folder" }));
    onRouteChange.mockClear();
    const menu = await openFolderMenu();
    await fireEvent.click(within(menu).getByRole("button", { name: "Remove Notes" }));
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
