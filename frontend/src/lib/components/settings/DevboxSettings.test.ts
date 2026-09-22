import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import type { Assignment, Connection, Discovery } from "../../api/generated/models/index.js";
import { createMockApiFetch, mockSettings } from "../../../test/mockApiFetch.js";
import { createSettingsStore } from "../../stores/settings.svelte.js";

const { store } = vi.hoisted(() => ({
  store: { current: undefined as unknown as ReturnType<typeof createSettingsStore> },
}));
vi.mock("../../context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../context.js")>()),
  getStores: () => ({ settings: store.current }),
}));

import DevboxSettings from "./DevboxSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

const assignment: Assignment = {
  host_id: "build-a",
  name: "Build A",
  account: "developer",
  github_user_id: 123,
  node_id: "a".repeat(32),
  protocol: 1,
  role: "execution-worker",
  uid: 1000,
  url: "http://build-a.example.ts.net:9100",
  ssh_address: "developer@build-a.example.ts.net",
  ssh_host_fingerprint: "SHA256:example",
  maintenance: false,
};
const connection: Connection = { ...assignment, id: "connection-a", registry_id: "registry-a", revision: "1" };
const discovery: Discovery = {
  registry_id: "registry-a",
  registry_url: "https://registry.example.test",
  github_user_id: 123,
  protocol: 1,
  revision: "1",
  devboxes: [assignment],
};
const selfHost = {
  configKey: "hub",
  diagnostics: [],
  id: "hub",
  kind: "self",
  name: "Studio",
  federationRole: "hub",
  operationAvailability: {},
  platform: "darwin",
  preferredTransport: "local",
  reachable: true,
  tmuxSessions: [],
};
function devboxHost(reachable: boolean) {
  return {
    ...selfHost,
    configKey: "devbox:connection-a",
    id: "devbox:connection-a",
    kind: "devbox",
    name: "Build A",
    federationRole: "devbox",
    platform: "linux",
    preferredTransport: "http",
    reachable,
    ...(reachable ? {} : { error: "devbox offline: dial tcp: connection refused" }),
  };
}

beforeEach(() => {
  store.current = createSettingsStore();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("lists machines once, shows live status, and selects the default in place", async () => {
  let connections: Connection[] = [];
  let saved = structuredClone(mockSettings);
  const api = createMockApiFetch([
    ({ method, url, bodyText }) => {
      if (url.pathname === "/api/v1/devboxes/discovery") return Response.json(discovery);
      if (url.pathname === "/api/v1/snapshot")
        return Response.json({
          hosts: connections.length > 0 ? [selfHost, devboxHost(true)] : [selfHost],
          workspaces: [],
        });
      if (url.pathname === "/api/v1/devboxes") {
        if (method === "POST") {
          connections = [connection];
          return Response.json(connection);
        }
        return Response.json(connections);
      }
      if (url.pathname === "/api/v1/settings") {
        if (method === "PUT")
          saved = { ...saved, workspaces: { ...saved.workspaces, ...JSON.parse(bodyText).workspaces } };
        return Response.json(saved);
      }
      if (method === "DELETE" && url.pathname === "/api/v1/devboxes/connection-a") {
        connections = [];
        return new Response(null, { status: 204 });
      }
    },
  ]);
  vi.stubGlobal("fetch", api.fetch);
  render(SettingsRuntimeHarness, { component: DevboxSettings, componentProps: {} });

  const list = screen.getByRole("list", { name: "Workspace machines" });
  const local = screen.getByRole("radio", { name: "Run new workspaces on this Forge machine" });
  expect((local as HTMLInputElement).checked).toBe(true);
  await screen.findByText("Not connected");
  expect((screen.getByRole("radio", { name: "Run new workspaces on Build A" }) as HTMLInputElement).disabled).toBe(
    true,
  );
  expect(screen.getByText("https://registry.example.test")).toBeTruthy();
  expect(screen.queryByLabelText("Registry address")).toBeNull();

  await fireEvent.click(await screen.findByRole("button", { name: "Connect Build A" }));
  await screen.findByRole("button", { name: "Manage Build A" });
  expect(within(list).getAllByText("Build A")).toHaveLength(1);
  expect(screen.queryByRole("button", { name: "Connect Build A" })).toBeNull();
  expect(screen.getByRole("status").textContent).toContain("Build A is connected");
  await within(list).findByText("Online");

  const remote = screen.getByRole("radio", { name: "Run new workspaces on Build A" }) as HTMLInputElement;
  expect(remote.disabled).toBe(false);
  await fireEvent.click(remote);
  await waitFor(() => expect(saved.workspaces.default_execution_target).toBe("devbox:connection-a"));
  await waitFor(() => expect(remote.checked).toBe(true));
  expect((local as HTMLInputElement).checked).toBe(false);

  await fireEvent.click(screen.getByRole("button", { name: "Refresh devboxes" }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Refresh devboxes" }).hasAttribute("disabled")).toBe(false),
  );
  expect(within(list).getAllByText("Build A")).toHaveLength(1);

  await fireEvent.click(screen.getByRole("button", { name: "Manage Build A" }));
  await fireEvent.click(screen.getByRole("menuitem", { name: "Disconnect" }));
  await screen.findByRole("button", { name: "Connect Build A" });
  expect(screen.getByText(/Your default machine is no longer connected/)).toBeTruthy();
  expect(saved.workspaces.default_execution_target).toBe("devbox:connection-a");
});

it("offers Reconnect for an unreachable devbox", async () => {
  const api = createMockApiFetch([
    ({ url }) => {
      if (url.pathname === "/api/v1/devboxes") return Response.json([connection]);
      if (url.pathname === "/api/v1/devboxes/discovery") return Response.json({ ...discovery, devboxes: [] });
      if (url.pathname === "/api/v1/snapshot")
        return Response.json({ hosts: [selfHost, devboxHost(false)], workspaces: [] });
    },
  ]);
  vi.stubGlobal("fetch", api.fetch);
  render(SettingsRuntimeHarness, { component: DevboxSettings, componentProps: {} });
  await screen.findByText("Offline");
  expect(screen.getByText(/connection refused/)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Reconnect Build A" })).toBeTruthy();
  expect(screen.queryByText(/No devboxes are assigned/)).toBeNull();
});

it("keeps saved machines visible when discovery fails, then explains an empty assignment list", async () => {
  let fail = true;
  const api = createMockApiFetch([
    ({ url }) => {
      if (url.pathname === "/api/v1/devboxes") return Response.json([connection]);
      if (url.pathname === "/api/v1/devboxes/discovery")
        return fail
          ? Response.json(
              {
                code: "forbidden",
                type: "about:blank",
                title: "Forbidden",
                detail: "Account is not enrolled",
                status: 403,
              },
              { status: 403, headers: { "Content-Type": "application/problem+json" } },
            )
          : Response.json({ ...discovery, devboxes: [] });
    },
  ]);
  vi.stubGlobal("fetch", api.fetch);
  const mounted = render(SettingsRuntimeHarness, { component: DevboxSettings, componentProps: {} });
  await screen.findByText(/Account is not enrolled/);
  expect(screen.getByRole("button", { name: "Manage Build A" })).toBeTruthy();
  expect(screen.getByLabelText("Registry address")).toBeTruthy();
  fail = false;
  mounted.unmount();
  vi.stubGlobal(
    "fetch",
    createMockApiFetch([
      ({ url }) => {
        if (url.pathname === "/api/v1/devboxes") return Response.json([]);
        if (url.pathname === "/api/v1/devboxes/discovery") return Response.json({ ...discovery, devboxes: [] });
      },
    ]).fetch,
  );
  render(SettingsRuntimeHarness, { component: DevboxSettings, componentProps: {} });
  await screen.findByText(/No devboxes are assigned to you yet/);
  expect(screen.queryByLabelText("Registry address")).toBeNull();
});
