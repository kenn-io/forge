import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import * as flash from "../../stores/flash.svelte.js";
import AddFolderDialogTestHarness from "./AddFolderDialogTestHarness.svelte";
import type { DocsAPI } from "../../api/docs/api";
import { createMockDocsBackend } from "./docsTestBackend";
import { setActiveKataDaemon, setKataDaemonRoster } from "../../stores/active-kata-daemon.svelte";

afterEach(() => {
  cleanup();
  setActiveKataDaemon(undefined);
  setKataDaemonRoster([], undefined);
  for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
});

function renderDialog(
  opts: {
    api?: DocsAPI;
    open?: boolean;
    initialPath?: string;
  } = {},
) {
  const api = opts.api ?? createMockDocsBackend();
  const onClose = vi.fn();
  const onAdded = vi.fn();
  const props = {
    open: opts.open ?? true,
    api,
    onClose,
    onAdded,
    initialPath: opts.initialPath ?? "",
  };
  const result = render(AddFolderDialogTestHarness, { props });
  return { ...result, api, onClose, onAdded, props };
}

describe("AddFolderDialog", () => {
  test("loads the browser with the synthetic home and lists subfolders", async () => {
    renderDialog();
    await waitFor(() => screen.getByText("Documents"));
    // Hidden by default
    expect(screen.queryByText(".config")).toBeNull();
    expect(screen.getByText("Notes")).toBeTruthy();
    expect(screen.getByText("Projects")).toBeTruthy();
  });

  test("toggling 'Show hidden' reveals dotted folders", async () => {
    renderDialog();
    await waitFor(() => screen.getByText("Documents"));
    const toggle = screen.getByLabelText(/Show hidden/);
    await fireEvent.click(toggle);
    expect(screen.getByText(".config")).toBeTruthy();
  });

  test("interrupts an obsolete folder browse when its initial path changes", async () => {
    const api = createMockDocsBackend();
    const originalBrowse = api.browseDirectories.bind(api);
    let firstSignal: AbortSignal | undefined;
    vi.spyOn(api, "browseDirectories").mockImplementation((path, signal) => {
      if (path !== "/first") return originalBrowse(path, signal);
      firstSignal = signal;
      return new Promise(() => {});
    });
    const rendered = renderDialog({ api, initialPath: "/first" });
    await waitFor(() => expect(api.browseDirectories).toHaveBeenCalledWith("/first", expect.any(AbortSignal)));

    await rendered.rerender({ ...rendered.props, initialPath: "/second" });

    await waitFor(() => expect(firstSignal?.aborted).toBe(true));
  });

  test("'Use this folder' copies the current browser path into the input", async () => {
    renderDialog();
    await waitFor(() => screen.getByText("Documents"));
    await fireEvent.click(screen.getByRole("button", { name: "Use this folder" }));
    const input = screen.getByPlaceholderText("~/Notes") as HTMLInputElement;
    expect(input.value).toBe("/Users/mock");
  });

  test("submit calls api.addFolder and fires onAdded with the result", async () => {
    const api = createMockDocsBackend();
    const addSpy = vi.spyOn(api, "addFolder");
    const { onAdded, onClose } = renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));
    const input = screen.getByPlaceholderText("~/Notes") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "/mock/new-folder" } });
    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));
    await waitFor(() => expect(addSpy).toHaveBeenCalled());
    expect(addSpy).toHaveBeenCalledWith({ path: "/mock/new-folder" }, expect.any(AbortSignal));
    expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ id: "new-folder", path: "/mock/new-folder" }));
    expect(onClose).toHaveBeenCalled();
  });

  test("finishes adding when the response is lost but the folder list confirms the path", async () => {
    const backend = createMockDocsBackend();
    const addFolder = vi.fn(async (input: Parameters<DocsAPI["addFolder"]>[0], signal?: AbortSignal) => {
      await backend.addFolder(input, signal);
      throw new Error("response lost after commit");
    });
    const api = { ...backend, addFolder };
    const { onAdded, onClose } = renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));
    await fireEvent.input(screen.getByPlaceholderText("~/Notes"), { target: { value: "/mock/recovered-folder" } });
    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));

    await waitFor(() =>
      expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ path: "/mock/recovered-folder" })),
    );
    expect(onClose).toHaveBeenCalledOnce();
  });

  test("reuses the original folder baseline when retrying retained uncertainty", async () => {
    const backend = createMockDocsBackend();
    let committed = false;
    let failedReconciliation = false;
    const addFolder = vi.fn(async (input: Parameters<DocsAPI["addFolder"]>[0], signal?: AbortSignal) => {
      const folder = await backend.addFolder(input, signal);
      committed = true;
      throw new Error(`response lost after adding ${folder.path}`);
    });
    const listFolders = vi.fn(async (signal?: AbortSignal) => {
      const folders = await backend.listFolders(signal);
      if (committed && !failedReconciliation) {
        failedReconciliation = true;
        throw new Error("folder reconciliation unavailable");
      }
      return folders;
    });
    const api = { ...backend, addFolder, listFolders };
    const { onAdded, onClose } = renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));
    await fireEvent.input(screen.getByPlaceholderText("~/Notes"), {
      target: { value: "/mock/retained-folder" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));
    await waitFor(() =>
      expect(flash.getFlash()).toMatchObject({
        message: expect.stringMatching(/could not be confirmed/i),
        tone: "danger",
      }),
    );

    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));

    await waitFor(() =>
      expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ path: "/mock/retained-folder" })),
    );
    expect(addFolder).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledOnce();
  });

  test("submits a selected daemon binding when multiple daemons are available", async () => {
    const api = createMockDocsBackend();
    const addSpy = vi.spyOn(api, "addFolder");
    setKataDaemonRoster(["home", "work"], "home");
    renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));

    await fireEvent.click(screen.getByRole("combobox", { name: /Daemon/ }));
    await fireEvent.click(screen.getByRole("option", { name: "work" }));
    const input = screen.getByPlaceholderText("~/Notes") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "/mock/shared-notes" } });
    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));

    await waitFor(() => expect(addSpy).toHaveBeenCalled());
    expect(addSpy).toHaveBeenCalledWith({ path: "/mock/shared-notes", daemon: "work" }, expect.any(AbortSignal));
  });

  test("leaves a multi-daemon folder unbound until a daemon is selected", async () => {
    const api = createMockDocsBackend();
    const addSpy = vi.spyOn(api, "addFolder");
    setKataDaemonRoster(["home", "work"], "home");
    renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));

    const input = screen.getByPlaceholderText("~/Notes") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "/mock/shared-notes" } });
    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));

    await waitFor(() => expect(addSpy).toHaveBeenCalled());
    expect(addSpy).toHaveBeenCalledWith({ path: "/mock/shared-notes" }, expect.any(AbortSignal));
  });

  test("drops a stale selected daemon when the roster shrinks before submit", async () => {
    const api = createMockDocsBackend();
    const addSpy = vi.spyOn(api, "addFolder");
    setKataDaemonRoster(["home", "work"], "home");
    renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));

    await fireEvent.click(screen.getByRole("combobox", { name: /Daemon/ }));
    await fireEvent.click(screen.getByRole("option", { name: "work" }));
    setKataDaemonRoster(["home"], "home");
    const input = screen.getByPlaceholderText("~/Notes") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "/mock/shared-notes" } });
    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));

    await waitFor(() => expect(addSpy).toHaveBeenCalled());
    expect(addSpy).toHaveBeenCalledWith({ path: "/mock/shared-notes" }, expect.any(AbortSignal));
  });

  test("server errors surface as an inline message and keep the dialog open", async () => {
    const api = createMockDocsBackend();
    vi.spyOn(api, "addFolder").mockRejectedValue(
      Object.assign(new Error("id taken"), { status: 409, code: "duplicate_folder_id" }),
    );
    const { onAdded, onClose } = renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));
    const input = screen.getByPlaceholderText("~/Notes") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "/mock/notes" } });
    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));
    const alert = await waitFor(() => screen.getByRole("alert"));
    expect(alert.textContent).toContain("id taken");
    expect(onAdded).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  test("generic add failures use the shared danger flash and keep the dialog open", async () => {
    const api = createMockDocsBackend();
    vi.spyOn(api, "addFolder").mockRejectedValue(new Error("daemon unavailable"));
    const { onAdded, onClose } = renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));
    await fireEvent.input(screen.getByPlaceholderText("~/Notes"), { target: { value: "/mock/notes" } });
    await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));

    await waitFor(() => expect(flash.getFlash()).toMatchObject({ message: "daemon unavailable", tone: "danger" }));
    expect(screen.queryByText("daemon unavailable")).toBeNull();
    expect(onAdded).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  test("advanced toggle reveals optional name and id fields", async () => {
    renderDialog();
    await waitFor(() => screen.getByText("Documents"));
    expect(screen.queryByText(/Display name/)).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: /advanced/i }));
    expect(screen.getByText(/Display name/)).toBeTruthy();
    expect(screen.getByText(/Folder id/)).toBeTruthy();
  });

  test("cancel button calls onClose without hitting the API", async () => {
    const api = createMockDocsBackend();
    const addSpy = vi.spyOn(api, "addFolder");
    const { onClose } = renderDialog({ api });
    await waitFor(() => screen.getByText("Documents"));
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalled();
    expect(addSpy).not.toHaveBeenCalled();
  });

  test("submit button stays disabled while the path field is empty", async () => {
    renderDialog();
    await waitFor(() => screen.getByText("Documents"));
    const submit = screen.getByRole("button", { name: "Add folder" });
    expect(submit.hasAttribute("disabled")).toBe(true);
  });
});
