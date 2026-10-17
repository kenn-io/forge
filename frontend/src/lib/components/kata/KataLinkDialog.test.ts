import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import { makeAppRuntime } from "../../app/runtime.js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import type { GeneratedClient } from "../../api/generated-api.js";
import { makeGeneratedClient } from "../../testing/generated-client.js";
import type { KataLinksSubject } from "../../stores/kata-links.svelte.js";
import KataLinkDialog from "./KataLinkDialog.svelte";

let runtime: ReturnType<typeof makeAppRuntime>;
const sent = vi.fn().mockResolvedValue({ status: "queued" });
vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtime }));

const subject: KataLinksSubject = { kind: "workspace", workspaceID: "workspace-1" };

function clientWith(
  listDaemons: ReturnType<typeof vi.fn>,
  listReferences = vi.fn(),
  createLink = vi.fn(),
): GeneratedClient {
  return makeGeneratedClient({
    KataService: {
      listKataDaemons: listDaemons,
      listKataReferences: listReferences,
      createWorkspaceKataLink: createLink,
    },
  });
}

function renderDialog(client: GeneratedClient, onlinked = vi.fn(), onclose = vi.fn()) {
  return {
    ...render(KataLinkDialog, {
      props: { subject, onlinked, onclose, apiClient: client },
    }),
    onlinked,
    onclose,
  };
}

describe("KataLinkDialog", () => {
  beforeEach(() => {
    sent.mockClear();
    runtime = makeAppRuntime(makeGeneratedClient({ SystemService: { captureTelemetryEvent: sent } }));
  });

  afterEach(async () => {
    cleanup();
    await Effect.runPromise(runtime.disposeEffect);
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("reports only successful empty Kata searches, including recovery", async () => {
    const listReferences = vi
      .fn()
      .mockResolvedValueOnce({
        issues: [{ uid: "issue-1", qualified_id: "TASK-1", title: "Found", project_name: "Example", status: "open" }],
      })
      .mockRejectedValueOnce(new Error("Read failed"))
      .mockResolvedValue({ issues: [] });
    const view = renderDialog(
      clientWith(
        vi.fn().mockResolvedValue({ daemons: [{ id: "healthy", health: "connected", default: true }] }),
        listReferences,
      ),
    );
    const search = await screen.findByRole("searchbox", { name: "Search Kata issues" });
    await waitFor(() => expect((search as HTMLInputElement).disabled).toBe(false));
    vi.useFakeTimers();
    await fireEvent.input(search, { target: { value: "found" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(screen.getByText("Found")).toBeTruthy();
    expect(sent).not.toHaveBeenCalled();
    await fireEvent.input(search, { target: { value: "absent" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(screen.getByRole("alert").textContent).toBe("Read failed");
    expect(sent).not.toHaveBeenCalled();
    await fireEvent.input(search, { target: { value: "absent" } });
    await vi.advanceTimersByTimeAsync(250);
    expect(screen.getByText("No matching Kata issues.")).toBeTruthy();
    expect(sent).toHaveBeenCalledTimes(1);
    expect(sent.mock.calls[0]?.[0]).toEqual({ event: "search_empty", properties: { surface: "web" } });
    expect(listReferences).toHaveBeenLastCalledWith(
      { daemonId: "healthy" },
      { q: "absent", limit: 50 },
      { signal: expect.any(AbortSignal) },
    );
    await vi.advanceTimersByTimeAsync(500);
    expect(sent).toHaveBeenCalledTimes(1);

    const replies: Array<(value: { issues: [] }) => void> = [];
    listReferences.mockImplementation(() => new Promise<{ issues: [] }>((resolve) => replies.push(resolve)));
    await fireEvent.input(search, { target: { value: "old" } });
    await vi.advanceTimersByTimeAsync(250);
    await fireEvent.input(search, { target: { value: "new" } });
    replies[0]!({ issues: [] });
    await vi.advanceTimersByTimeAsync(250);
    expect(sent).toHaveBeenCalledTimes(1);
    view.unmount();
    replies[1]!({ issues: [] });
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveBeenCalledTimes(1);
  });

  it("uses the configured default initially and disables unhealthy daemons", async () => {
    const get = vi.fn().mockResolvedValue({
      daemons: [
        {
          id: "healthy",
          url: "http://healthy",
          health: "connected",
          auth: "none",
          default: true,
          api_schema_version: "0.13.0",
        },
        {
          id: "down",
          url: "http://down",
          health: "unreachable",
          auth: "none",
          default: false,
          api_schema_version: "0.13.0",
        },
      ],
    });
    renderDialog(clientWith(get));

    const trigger = await screen.findByRole("combobox", { name: /Kata daemon: healthy/ });
    await fireEvent.click(trigger);
    expect((screen.getByRole("option", { name: /down/ }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("searchbox", { name: "Search Kata issues" }) as HTMLInputElement).disabled).toBe(false);
    expect(screen.queryByLabelText(/daemon URL/i)).toBeNull();
    expect(screen.queryByLabelText(/issue UID/i)).toBeNull();
  });

  it("debounces reference search and submits the selected canonical identity", async () => {
    const get = vi.fn();
    const listDaemons = vi.fn(() => {
      get("listKataDaemons");
      return Promise.resolve({
        daemons: [
          {
            id: "healthy",
            url: "http://healthy",
            health: "connected",
            auth: "none",
            default: true,
            api_schema_version: "0.10.0",
          },
        ],
      });
    });
    const listReferences = vi.fn(() => {
      get("listKataReferences");
      return Promise.resolve({
        issues: [
          {
            uid: "issue-1",
            project_uid: "project-1",
            project_name: "Kata",
            qualified_id: "KATA-1",
            short_id: "KT-1",
            status: "open",
            title: "Keep one UI",
          },
        ],
      });
    });
    const post = vi.fn().mockResolvedValue({ state: "complete", diagnostics: [], links: [] });
    const rendered = renderDialog(clientWith(listDaemons, listReferences, post));
    await screen.findByRole("combobox", { name: /Kata daemon: healthy/ });
    vi.useFakeTimers();

    await fireEvent.input(screen.getByRole("searchbox", { name: "Search Kata issues" }), {
      target: { value: "keep" },
    });
    await vi.advanceTimersByTimeAsync(249);
    expect(get).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(get).toHaveBeenCalledTimes(2);

    await fireEvent.click(await screen.findByRole("button", { name: /KATA-1 Keep one UI/ }));
    await fireEvent.click(screen.getByRole("button", { name: "Link issue" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post).toHaveBeenCalledWith(
      { id: "workspace-1" },
      { daemon_id: "healthy", issue_uid: "issue-1", project_uid: "project-1" },
    );
    expect(rendered.onlinked).toHaveBeenCalledTimes(1);
    expect(rendered.onclose).toHaveBeenCalledTimes(1);
  });
});
