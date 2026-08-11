import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { tick } from "svelte";

import { projectIssueDetail } from "@kenn-io/kata-ui/packages/kata-ui/src/index.ts";

import type { components } from "../../api/generated/schema.js";
import type { GeneratedClient } from "../../api/generated-api.js";
import { NAVIGATE_KEY } from "../../context.js";
import type { KataLinksSubject } from "../../stores/kata-links.svelte.js";
import { resetKataWorkspaceCreateForTest } from "../../stores/kata-workspace-create.svelte.js";
import KataLinksPanel from "./KataLinksPanel.svelte";

vi.mock("@kenn-io/kata-ui/packages/kata-ui/src/index.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@kenn-io/kata-ui/packages/kata-ui/src/index.ts")>();
  return { ...actual, projectIssueDetail: vi.fn(actual.projectIssueDetail) };
});

type Link = components["schemas"]["KataEffectiveLink"];
type LinksResponse = components["schemas"]["KataEffectiveLinksResponse"];

const subject: KataLinksSubject = { kind: "workspace", workspaceID: "workspace-1" };

function link(overrides: Partial<Link> = {}): Link {
  return {
    daemon_id: "daemon-a",
    daemon_health: "ok",
    api_schema_version: "0.10.0",
    issue_uid: "issue-1",
    project_uid: "project-1",
    provenance: ["direct"],
    reference: "KT-1",
    status: "open",
    title: "Keep one Kata UI",
    direct_link_id: 41,
    ...overrides,
  };
}

function response(links: Link[], overrides: Partial<LinksResponse> = {}): LinksResponse {
  return { state: "complete", diagnostics: [], links, ...overrides };
}

function detail(apiSchemaVersion = "0.10.0", title = "Keep one Kata UI") {
  return {
    api_schema_version: apiSchemaVersion,
    daemon_health: "ok",
    detail: {
      issue: {
        uid: "issue-1",
        project_uid: "project-1",
        qualified_id: "KT-1",
        title,
        body: "Use the shared component.",
        status: "open",
      },
    },
  };
}

function forgeClient(methods: Partial<GeneratedClient>): GeneratedClient {
  return {
    GET: vi.fn(),
    POST: vi.fn(),
    PUT: vi.fn(),
    PATCH: vi.fn(),
    DELETE: vi.fn(),
    OPTIONS: vi.fn(),
    HEAD: vi.fn(),
    TRACE: vi.fn(),
    ...methods,
  } as unknown as GeneratedClient;
}

function renderPanel(client: GeneratedClient, props: { active?: boolean; disabled?: boolean } = {}) {
  const navigate = vi.fn();
  const rendered = render(KataLinksPanel, {
    props: { subject, active: props.active ?? true, disabled: props.disabled ?? false, apiClient: client },
    context: new Map<symbol, unknown>([[NAVIGATE_KEY, navigate]]),
  });
  return { ...rendered, navigate };
}

function popupFixture() {
  const popupDocument = document.implementation.createHTMLDocument("Kata launch");
  const replace = vi.fn();
  const close = vi.fn();
  const popup = {
    close,
    document: popupDocument,
    location: { replace },
    opener: window,
  } as unknown as Window;
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  return { popup, popupDocument, replace, close };
}

function listAndDetailClient(links: Link[], detailEnvelope = detail(), linksResponse?: Partial<LinksResponse>) {
  const get = vi.fn().mockImplementation((path: string) => {
    if (path === "/workspaces/{id}/kata-links") {
      return Promise.resolve({ data: response(links, linksResponse) });
    }
    if (path === "/kata/daemons/{daemon_id}/issues/{issue_uid}") {
      return Promise.resolve({ data: structuredClone(detailEnvelope) });
    }
    throw new Error(`unexpected GET ${path}`);
  });
  return { client: forgeClient({ GET: get }), get };
}

describe("KataLinksPanel", () => {
  beforeEach(() => {
    vi.mocked(projectIssueDetail).mockClear();
    vi.mocked(projectIssueDetail).mockImplementation((wire) => ({
      issue: {
        uid: wire.issue.uid,
        projectUID: wire.issue.project_uid ?? "",
        projectName: wire.issue.project_name ?? "",
        reference: wire.issue.qualified_id ?? wire.issue.short_id ?? wire.issue.uid,
        title: wire.issue.title,
        body: wire.issue.body ?? "",
        status: wire.issue.status,
        checklist: [],
        labels: [],
      },
      comments: [],
      links: [],
      children: [],
      pendingClaims: [],
    }));
  });

  afterEach(() => {
    resetKataWorkspaceCreateForTest();
    cleanup();
    vi.restoreAllMocks();
  });

  it("offers explicit linking from the empty state", async () => {
    const { client } = listAndDetailClient([]);
    renderPanel(client);

    expect(await screen.findByText("No Kata issues linked yet.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Link Kata issue" })).toBeTruthy();
  });

  it("defers the initial load until activation and loads the subject only once", async () => {
    const { client, get } = listAndDetailClient([link()]);
    const panel = renderPanel(client, { active: false });

    await tick();
    expect(get).not.toHaveBeenCalled();

    await panel.rerender({ subject, active: true, apiClient: client });
    expect(await screen.findByLabelText("Kata issue detail")).toBeTruthy();
    expect(get).toHaveBeenCalledTimes(2);

    await panel.rerender({ subject, active: false, apiClient: client });
    await panel.rerender({ subject, active: true, apiClient: client });
    await tick();
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("lists multiple links with daemon and combined provenance disambiguation", async () => {
    const links = [
      link({ provenance: ["intrinsic", "direct", "inherited"] }),
      link({ daemon_id: "daemon-b", issue_uid: "issue-2", reference: "KT-2", title: "Second task" }),
    ];
    const { client } = listAndDetailClient(links);
    renderPanel(client);

    expect(await screen.findByRole("button", { name: /KT-1 Keep one Kata UI/ })).toBeTruthy();
    expect(screen.getByText("daemon-a")).toBeTruthy();
    expect(screen.getByText("Intrinsic, Direct, Inherited")).toBeTruthy();
    expect(screen.getByRole("button", { name: /KT-2 Second task/ })).toBeTruthy();
    expect(screen.getByText("daemon-b")).toBeTruthy();
  });

  it("only exposes unlink for a direct link and preserves controls after deletion", async () => {
    const direct = link();
    const inherited = link({
      daemon_id: "daemon-b",
      issue_uid: "issue-2",
      reference: "KT-2",
      title: "Second task",
      direct_link_id: undefined,
      provenance: ["inherited"],
    });
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: response([direct, inherited]) })
      .mockResolvedValueOnce({ data: detail() })
      .mockResolvedValueOnce({ data: response([inherited]) })
      .mockResolvedValueOnce({ data: detail("0.10.0", "Second task") });
    const remove = vi.fn().mockResolvedValue({ data: undefined });
    renderPanel(forgeClient({ GET: get, DELETE: remove }));

    await screen.findByRole("button", { name: "Unlink KT-1" });
    expect(screen.queryByRole("button", { name: "Unlink KT-2" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Unlink KT-1" }));

    await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
    expect(remove).toHaveBeenCalledWith("/workspaces/{id}/kata-links/{link_id}", {
      params: { path: { id: "workspace-1", link_id: 41 } },
    });
    expect(await screen.findByRole("button", { name: /KT-2 Second task/ })).toBeTruthy();
  });

  it("keeps unavailable persisted links visible and reports a total hydration outage", async () => {
    const unavailable = link({ unavailable_reason: "daemon unreachable", title: undefined });
    const { client } = listAndDetailClient([unavailable], detail(), {
      state: "unavailable",
      diagnostics: [{ daemon_id: "daemon-a", reason: "connection refused" }],
    });
    renderPanel(client);

    expect(await screen.findByText("Unavailable")).toBeTruthy();
    expect(await screen.findAllByText("daemon unreachable")).toHaveLength(2);
    expect(screen.getByRole("status").textContent).toContain("daemon-a: connection refused");
    expect(screen.getByRole("button", { name: "Unlink KT-1" })).toBeTruthy();
  });

  it("explains why workspace creation is unavailable for the selected task", async () => {
    const unavailableWorkspace: NonNullable<Link["workspace"]> = {
      available: false,
      resolution_status: "ambiguous",
      resolution_source: "configured_clone",
      unavailable_reason: "Multiple repositories match this Kata project. Configure an explicit mapping in Settings.",
    };
    const { client } = listAndDetailClient([link({ workspace: unavailableWorkspace })]);
    renderPanel(client);

    expect(await screen.findByText(unavailableWorkspace.unavailable_reason ?? "")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Create workspace" })).toBeNull();
  });

  it("opens an existing workspace when live workspace creation is unavailable", async () => {
    const existing = link({
      unavailable_reason: "daemon unavailable",
      workspace: {
        available: false,
        existing_workspace: { id: "workspace-existing", status: "ready" },
      },
    });
    const { client } = listAndDetailClient([existing]);
    const panel = renderPanel(client);

    await fireEvent.click(await screen.findByRole("button", { name: "Open workspace" }));

    expect(panel.navigate).toHaveBeenCalledWith("/terminal/workspace-existing");
  });

  it("disables opening an existing workspace while the surrounding surface is disabled", async () => {
    const existing = link({
      unavailable_reason: "daemon unavailable",
      workspace: {
        available: false,
        existing_workspace: { id: "workspace-existing", status: "ready" },
      },
    });
    const { client } = listAndDetailClient([existing]);
    const panel = renderPanel(client, { disabled: true });

    const button = await screen.findByRole("button", { name: "Open workspace" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(button);

    expect(panel.navigate).not.toHaveBeenCalled();
  });

  it("disables workspace creation while the surrounding surface is disabled", async () => {
    const creatable = link({ workspace: { available: true } });
    const { client } = listAndDetailClient([creatable]);
    const post = vi.mocked(client.POST);
    renderPanel(client, { disabled: true });

    const button = await screen.findByRole("button", { name: "Create workspace" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(button);

    expect(post).not.toHaveBeenCalled();
  });

  it.each([undefined, "0.8.9", "0.11.0"])(
    "does not project or render incompatible schema version %s",
    async (version) => {
      const envelope = detail();
      envelope.api_schema_version = version;
      const { client } = listAndDetailClient([link({ api_schema_version: version })], envelope);
      renderPanel(client);

      expect(await screen.findByText(/Kata API schema is not supported/)).toBeTruthy();
      expect(projectIssueDetail).not.toHaveBeenCalled();
      expect(screen.queryByLabelText("Kata issue detail")).toBeNull();
    },
  );

  it.each(["0.9.0", "0.10.7"])("renders supported schema version %s through Kata UI", async (version) => {
    const { client } = listAndDetailClient([link({ api_schema_version: version })], detail(version));
    renderPanel(client);

    expect(await screen.findByLabelText("Kata issue detail")).toBeTruthy();
    expect(projectIssueDetail).toHaveBeenCalledTimes(1);
  });

  it("manually refreshes links and selected detail", async () => {
    const { client, get } = listAndDetailClient([link()]);
    renderPanel(client);
    await screen.findByLabelText("Kata issue detail");

    await fireEvent.click(screen.getByRole("button", { name: "Refresh Kata issue" }));
    await waitFor(() => expect(get).toHaveBeenCalledTimes(4));
  });

  it("opens the selected issue with the pinned daemon launch target", async () => {
    const { popup, popupDocument, replace } = popupFixture();
    const open = vi.spyOn(window, "open").mockReturnValue(popup);
    const get = vi.fn().mockImplementation((path: string) => {
      if (path === "/workspaces/{id}/kata-links") return Promise.resolve({ data: response([link()]) });
      if (path === "/kata/daemons/{daemon_id}/issues/{issue_uid}") return Promise.resolve({ data: detail() });
      if (path === "/kata/daemons/{daemon_id}/issues/{issue_uid}/launch-target") {
        return Promise.resolve({ data: { available: true, url: "http://127.0.0.1:4222/issues/issue-1" } });
      }
      throw new Error(`unexpected GET ${path}`);
    });
    renderPanel(forgeClient({ GET: get }));
    await screen.findByLabelText("Kata issue detail");

    await fireEvent.click(screen.getByRole("button", { name: "Open in Kata" }));

    expect(open).toHaveBeenCalledWith("about:blank", "_blank");
    await waitFor(() => expect(popupDocument.body.querySelector("a")).not.toBeNull());
    const launch = popupDocument.body.querySelector("a");
    expect(launch?.href).toBe("http://127.0.0.1:4222/issues/issue-1");
    expect(launch?.referrerPolicy).toBe("no-referrer");
    expect(launch?.rel).toBe("noreferrer");
    expect(popupDocument.head.querySelector('meta[name="referrer"]')?.getAttribute("content")).toBe("no-referrer");
    expect(replace).not.toHaveBeenCalled();
    expect(popup.opener).toBeNull();
  });

  it("rejects an unsafe daemon launch target before navigating the popup", async () => {
    const { popup, popupDocument } = popupFixture();
    vi.spyOn(window, "open").mockReturnValue(popup);
    const get = vi.fn().mockImplementation((path: string) => {
      if (path === "/workspaces/{id}/kata-links") return Promise.resolve({ data: response([link()]) });
      if (path === "/kata/daemons/{daemon_id}/issues/{issue_uid}") return Promise.resolve({ data: detail() });
      if (path === "/kata/daemons/{daemon_id}/issues/{issue_uid}/launch-target") {
        return Promise.resolve({ data: { available: true, url: "javascript:alert(document.cookie)" } });
      }
      throw new Error(`unexpected GET ${path}`);
    });
    renderPanel(forgeClient({ GET: get }));
    await screen.findByLabelText("Kata issue detail");

    await fireEvent.click(screen.getByRole("button", { name: "Open in Kata" }));

    await waitFor(() => expect(popup.close).toHaveBeenCalledTimes(1));
    expect(popupDocument.body.querySelector("a")).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain("safe HTTP or HTTPS URL");
  });

  it("closes a reserved popup when selection changes during launch lookup", async () => {
    let resolveLaunch!: (value: { data: { available: true; url: string } }) => void;
    const launch = new Promise<{ data: { available: true; url: string } }>((resolve) => {
      resolveLaunch = resolve;
    });
    const { popup, popupDocument, close } = popupFixture();
    vi.spyOn(window, "open").mockReturnValue(popup);
    const links = [link(), link({ issue_uid: "issue-2", reference: "KT-2", title: "Second task" })];
    const get = vi.fn().mockImplementation((path: string, options: { params?: { path?: { issue_uid?: string } } }) => {
      if (path === "/workspaces/{id}/kata-links") return Promise.resolve({ data: response(links) });
      if (path === "/kata/daemons/{daemon_id}/issues/{issue_uid}") {
        return Promise.resolve({
          data: detail("0.10.0", options.params?.path?.issue_uid === "issue-2" ? "Second task" : "Keep one Kata UI"),
        });
      }
      if (path === "/kata/daemons/{daemon_id}/issues/{issue_uid}/launch-target") return launch;
      throw new Error(`unexpected GET ${path}`);
    });
    renderPanel(forgeClient({ GET: get }));
    await screen.findByLabelText("Kata issue detail");

    await fireEvent.click(screen.getByRole("button", { name: "Open in Kata" }));
    await fireEvent.click(screen.getByRole("button", { name: /KT-2 Second task/ }));
    resolveLaunch({ data: { available: true, url: "https://kata.example.test/issues/issue-1" } });

    await waitFor(() => expect(close).toHaveBeenCalledTimes(1));
    expect(popupDocument.body.querySelector("a")).toBeNull();
    expect(screen.getByRole("button", { name: "Open in Kata" })).toBeTruthy();
  });

  it("creates or opens the mapped workspace", async () => {
    const createLink = link({
      project_name: "Forge",
      workspace: { available: true, item_key: "item-key", item_type: "kata_task" },
    });
    const createClient = listAndDetailClient([createLink]);
    const post = vi.fn().mockResolvedValue({ data: { id: "workspace-new" } });
    const created = renderPanel(forgeClient({ GET: createClient.get, POST: post }));
    await screen.findByLabelText("Kata issue detail");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await waitFor(() => expect(created.navigate).toHaveBeenCalledWith("/terminal/workspace-new"));
    expect(post).toHaveBeenCalledWith("/kata/workspaces", {
      body: expect.objectContaining({ project_name: "Forge" }),
    });
    created.unmount();

    const existing = link({
      workspace: { available: true, existing_workspace: { id: "workspace-existing", status: "ready" } },
    });
    const existingClient = listAndDetailClient([existing]);
    const opened = renderPanel(existingClient.client);
    await screen.findByLabelText("Kata issue detail");
    await fireEvent.click(screen.getByRole("button", { name: "Open workspace" }));
    expect(opened.navigate).toHaveBeenCalledWith("/terminal/workspace-existing");
  });

  it("shares pending Kata workspace creation across remounts and only navigates the current panel", async () => {
    let resolveCreate!: (value: { data: { id: string; status: string } }) => void;
    const post = vi.fn(
      () =>
        new Promise<{ data: { id: string; status: string } }>((resolve) => {
          resolveCreate = resolve;
        }),
    );
    const createLink = link({
      issue_uid: "issue-remount",
      reference: "KT-remount",
      workspace: { available: true, item_key: "item-remount", item_type: "kata_task" },
    });
    const firstClient = listAndDetailClient([createLink]);
    const first = renderPanel(forgeClient({ GET: firstClient.get, POST: post }));
    await screen.findByLabelText("Kata issue detail");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    expect(post).toHaveBeenCalledTimes(1);
    first.unmount();

    const secondClient = listAndDetailClient([createLink]);
    const second = renderPanel(forgeClient({ GET: secondClient.get, POST: post }));
    await screen.findByLabelText("Kata issue detail");
    const pendingButton = screen.getByRole("button", { name: "Creating…" });
    expect((pendingButton as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(pendingButton);
    expect(post).toHaveBeenCalledTimes(1);

    resolveCreate({ data: { id: "workspace-remount", status: "ready" } });

    const openButton = await screen.findByRole("button", { name: "Open workspace" });
    expect(first.navigate).not.toHaveBeenCalled();
    await fireEvent.click(openButton);
    expect(second.navigate).toHaveBeenCalledWith("/terminal/workspace-remount");
  });

  it("does not let an A-to-B-to-A selection cycle reclaim an older workspace request", async () => {
    let resolveCreate!: (value: { data: { id: string; status: string } }) => void;
    const post = vi.fn(
      () =>
        new Promise<{ data: { id: string; status: string } }>((resolve) => {
          resolveCreate = resolve;
        }),
    );
    const workspace = { available: true, item_key: "item-key", item_type: "kata_task" } as const;
    const links = [
      link({ workspace }),
      link({ issue_uid: "issue-2", reference: "KT-2", title: "Second task", workspace }),
    ];
    const get = vi.fn().mockImplementation((path: string, options: { params?: { path?: { issue_uid?: string } } }) => {
      if (path === "/workspaces/{id}/kata-links") return Promise.resolve({ data: response(links) });
      if (path === "/kata/daemons/{daemon_id}/issues/{issue_uid}") {
        return Promise.resolve({
          data: detail("0.10.0", options.params?.path?.issue_uid === "issue-2" ? "Second task" : "Keep one Kata UI"),
        });
      }
      throw new Error(`unexpected GET ${path}`);
    });
    const panel = renderPanel(forgeClient({ GET: get, POST: post }));
    await screen.findByLabelText("Kata issue detail");
    await fireEvent.click(screen.getByRole("button", { name: "Create workspace" }));
    await fireEvent.click(screen.getByRole("button", { name: /KT-2 Second task/ }));
    await fireEvent.click(screen.getByRole("button", { name: /KT-1 Keep one Kata UI/ }));

    resolveCreate({ data: { id: "workspace-cycle", status: "ready" } });

    await screen.findByRole("button", { name: "Open workspace" });
    expect(panel.navigate).not.toHaveBeenCalled();
  });

  it("ignores an unlink failure after the selected Kata issue changes", async () => {
    let resolveDelete!: (value: { error: { detail: string } }) => void;
    const remove = vi.fn(
      () =>
        new Promise<{ error: { detail: string } }>((resolve) => {
          resolveDelete = resolve;
        }),
    );
    const links = [link(), link({ issue_uid: "issue-2", reference: "KT-2", title: "Second task" })];
    const { get } = listAndDetailClient(links);
    renderPanel(forgeClient({ GET: get, DELETE: remove }));
    await screen.findByLabelText("Kata issue detail");

    await fireEvent.click(screen.getByRole("button", { name: "Unlink KT-1" }));
    await fireEvent.click(screen.getByRole("button", { name: /KT-2 Second task/ }));
    resolveDelete({ error: { detail: "Old unlink failed." } });

    await waitFor(() => expect(screen.getByRole("button", { name: "Open in Kata" })).toBeTruthy());
    expect(screen.queryByText("Old unlink failed.")).toBeNull();
  });

  it("keeps Forge link controls visible when Kata projection throws", async () => {
    vi.mocked(projectIssueDetail).mockImplementationOnce(() => {
      throw new Error("package projection failed");
    });
    const { client } = listAndDetailClient([link()]);
    renderPanel(client);

    expect(await screen.findByText("Kata issue detail is unavailable.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Unlink KT-1" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open in Kata" })).toBeTruthy();
  });

  it("recovers the detail boundary after a successful refresh", async () => {
    vi.mocked(projectIssueDetail)
      .mockImplementationOnce(() => {
        throw new Error("package projection failed");
      })
      .mockImplementation((wire) => ({
        issue: {
          uid: wire.issue.uid,
          projectUID: wire.issue.project_uid ?? "",
          projectName: wire.issue.project_name ?? "",
          reference: wire.issue.qualified_id ?? wire.issue.uid,
          title: wire.issue.title,
          body: wire.issue.body ?? "",
          status: wire.issue.status,
          checklist: [],
          labels: [],
        },
        comments: [],
        links: [],
        children: [],
        pendingClaims: [],
      }));
    const { client } = listAndDetailClient([link()]);
    renderPanel(client);
    expect(await screen.findByText("Kata issue detail is unavailable.")).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Refresh Kata issue" }));

    expect(await screen.findByLabelText("Kata issue detail")).toBeTruthy();
  });
});
