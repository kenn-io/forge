import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { GeneratedClient } from "../../api/generated-api.js";
import type { KataLinksSubject } from "../../stores/kata-links.svelte.js";
import KataLinkDialog from "./KataLinkDialog.svelte";

const subject: KataLinksSubject = { kind: "workspace", workspaceID: "workspace-1" };

function clientWith(get: ReturnType<typeof vi.fn>, post = vi.fn()): GeneratedClient {
  return {
    GET: get,
    POST: post,
    PUT: vi.fn(),
    PATCH: vi.fn(),
    DELETE: vi.fn(),
    OPTIONS: vi.fn(),
    HEAD: vi.fn(),
    TRACE: vi.fn(),
  } as unknown as GeneratedClient;
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
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("uses the configured default initially and disables unhealthy or incompatible daemons", async () => {
    const get = vi.fn().mockResolvedValue({
      data: {
        daemons: [
          {
            id: "healthy",
            url: "http://healthy",
            health: "connected",
            auth: "none",
            default: true,
            api_schema_version: "0.10.0",
          },
          {
            id: "down",
            url: "http://down",
            health: "unreachable",
            auth: "none",
            default: false,
            api_schema_version: "0.10.0",
          },
          {
            id: "old",
            url: "http://old",
            health: "connected",
            auth: "none",
            default: false,
            api_schema_version: "0.8.4",
          },
        ],
      },
    });
    renderDialog(clientWith(get));

    const trigger = await screen.findByRole("combobox", { name: /Kata daemon: healthy/ });
    await fireEvent.click(trigger);
    expect((screen.getByRole("option", { name: /down/ }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("option", { name: /old/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByLabelText(/daemon URL/i)).toBeNull();
    expect(screen.queryByLabelText(/issue UID/i)).toBeNull();
  });

  it("debounces reference search and submits the selected canonical identity", async () => {
    const get = vi.fn().mockImplementation((path: string) => {
      if (path === "/kata/daemons") {
        return Promise.resolve({
          data: {
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
          },
        });
      }
      if (path === "/kata/daemons/{daemon_id}/references") {
        return Promise.resolve({
          data: {
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
          },
        });
      }
      throw new Error(`unexpected GET ${path}`);
    });
    const post = vi.fn().mockResolvedValue({ data: { state: "complete", diagnostics: [], links: [] } });
    const rendered = renderDialog(clientWith(get, post));
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
    expect(post).toHaveBeenCalledWith("/workspaces/{id}/kata-links", {
      params: { path: { id: "workspace-1" } },
      body: { daemon_id: "healthy", issue_uid: "issue-1", project_uid: "project-1" },
    });
    expect(rendered.onlinked).toHaveBeenCalledTimes(1);
    expect(rendered.onclose).toHaveBeenCalledTimes(1);
  });
});
