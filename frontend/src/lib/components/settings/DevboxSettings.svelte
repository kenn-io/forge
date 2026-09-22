<script lang="ts">
  import { onMount } from "svelte";
  import { Effect } from "effect";
  import { Button, TextInput, Menu, MenuTrigger, MenuContent, MenuItem } from "@kenn-io/kit-ui";
  import EllipsisIcon from "@lucide/svelte/icons/ellipsis";
  import ServerIcon from "@lucide/svelte/icons/server";
  import MonitorIcon from "@lucide/svelte/icons/monitor";
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import { getStores } from "../../context.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import { loadSnapshotHosts } from "../../api/fleet-snapshot.js";
  import type { Connection, Discovery, HostSummary } from "../../api/generated/models/index.js";
  import { saveWorkspaceSettings } from "../../stores/workspace-settings-persistence.js";

  const { settings } = getStores();
  const runtime = getAppRuntime();
  let registry = $state("");
  let connections = $state.raw<Connection[]>([]);
  let discovery = $state.raw<Discovery | null>(null);
  let hosts = $state.raw<HostSummary[] | null>(null);
  let discoveredRegistry = "";
  let pending = $state("");
  let checkingHosts = $state(false);
  let registryOpen = $state(false);
  let discoveryError = $state("");
  let error = $state("");
  let message = $state("");
  const busy = $derived(pending !== "");
  const available = $derived((discovery?.devboxes ?? []).filter((assignment) =>
    !connections.some((connection) => connection.registry_id === discovery?.registry_id
      && connection.host_id === assignment.host_id && connection.github_user_id === assignment.github_user_id),
  ));
  const preferred = $derived(settings.getWorkspaceSettings().default_execution_target ?? "");
  const preferredMissing = $derived(preferred !== "" && !connections.some((connection) => `devbox:${connection.id}` === preferred));
  // Writable derived: the radio group edits it locally, and a saved or hydrated preference re-derives it.
  let choice = $derived(preferred);
  const selfHost = $derived(hosts?.find((host) => host.kind === "self") ?? null);

  function status(connection: Connection): { label: string; detail: string; offline: boolean } {
    if (pending === `reconnect:${connection.id}`) return { label: "Reconnecting…", detail: "", offline: false };
    if (pending === `disconnect:${connection.id}`) return { label: "Disconnecting…", detail: "", offline: false };
    if (connection.maintenance) return { label: "Maintenance", detail: "Your operator has paused new work here.", offline: false };
    if (checkingHosts || hosts === null) return { label: "Checking…", detail: "", offline: false };
    const host = hosts.find((item) => item.configKey === `devbox:${connection.id}`);
    if (host?.reachable) return { label: "Online", detail: "", offline: false };
    return { label: "Offline", detail: host?.error ?? "Forge could not reach this devbox.", offline: true };
  }

  function load() {
    return executeGeneratedApiRequest("list devboxes", (client, signal) => client.DevboxesService.listDevboxConnections({ signal })).pipe(
      Effect.tap((items) => Effect.sync(() => { connections = items; })),
    );
  }

  function checkHosts(): void {
    checkingHosts = true;
    runtime.runCommand(
      loadSnapshotHosts().pipe(
        Effect.tap((items) => Effect.sync(() => { hosts = items; })),
        Effect.asVoid,
        Effect.ensuring(Effect.sync(() => { checkingHosts = false; })),
      ),
      { operation: "check devbox status", safeContext: {}, onFailure: () => { hosts = []; } },
    );
  }

  onMount(() => {
    checkHosts();
    const execution = discover();
    return () => execution.interrupt();
  });

  function discover() {
    pending = "discover";
    error = "";
    message = "";
    discoveryError = "";
    const requestedRegistry = registry.trim();
    return runtime.runCommand(
      load().pipe(
        Effect.andThen(() => executeGeneratedApiRequest("discover devboxes", (client, signal) => client.DevboxesService.discoverDevboxes({ registry: requestedRegistry }, { signal })).pipe(
          Effect.tap((result) => Effect.sync(() => { discovery = result; discoveredRegistry = requestedRegistry; registryOpen = false; })),
          Effect.catch((failure) => Effect.sync(() => {
            discovery = null;
            discoveryError = failure._tag === "ApiProblemError" ? (failure.problem.detail ?? failure.problem.title ?? "Discovery failed.") : failure.message;
            registryOpen = true;
          })),
        )),
        Effect.asVoid,
        Effect.ensuring(Effect.sync(() => { pending = ""; })),
      ),
      { operation: "discover devboxes", safeContext: {}, onFailure: (failure) => { error = failure._tag === "ApiProblemError" ? (failure.problem.detail ?? failure.problem.title ?? "Could not load saved devboxes.") : failure.message; } },
    );
  }

  function connect(hostId: string): void {
    pending = `connect:${hostId}`;
    error = "";
    message = "";
    runtime.runCommand(
      executeGeneratedApiRequest("connect devbox", (client, signal) => client.DevboxesService.connectDevbox({ registry: discoveredRegistry, host_id: hostId }, { signal })).pipe(
        Effect.tap((connection) => Effect.sync(() => {
          connections = [...connections.filter((item) => item.id !== connection.id), connection];
          message = `${connection.name} is connected. Select it to run new workspaces there.`;
          checkHosts();
        })),
        Effect.asVoid,
        Effect.ensuring(Effect.sync(() => { pending = ""; })),
      ),
      { operation: "connect devbox", safeContext: {}, onFailure: (failure) => { error = failure._tag === "ApiProblemError" ? (failure.problem.detail ?? failure.problem.title ?? "Devbox request failed.") : failure.message; } },
    );
  }

  function reconnect(connection: Connection): void {
    pending = `reconnect:${connection.id}`;
    error = "";
    message = "";
    runtime.runCommand(
      executeGeneratedApiRequest("reconnect devbox", (client, signal) => client.DevboxesService.reconnectDevbox({ connectionId: connection.id }, { signal })).pipe(
        Effect.andThen(() => load()),
        Effect.tap(() => Effect.sync(() => { message = `${connection.name} reconnected.`; checkHosts(); })), Effect.asVoid,
        Effect.ensuring(Effect.sync(() => { pending = ""; })),
      ),
      { operation: "reconnect devbox", safeContext: {}, onFailure: (failure) => { error = failure._tag === "ApiProblemError" ? (failure.problem.detail ?? failure.problem.title ?? "Devbox request failed.") : failure.message; } },
    );
  }

  function disconnect(id: string): void {
    pending = `disconnect:${id}`;
    error = "";
    message = "";
    runtime.runCommand(
      executeGeneratedApiRequest("disconnect devbox", (client, signal) => client.DevboxesService.disconnectDevbox({ connectionId: id }, { signal })).pipe(
        Effect.tap(() => Effect.sync(() => { connections = connections.filter((item) => item.id !== id); message = "Devbox disconnected. Its workspaces and agents remain on the devbox."; })), Effect.asVoid,
        Effect.ensuring(Effect.sync(() => { pending = ""; })),
      ),
      { operation: "disconnect devbox", safeContext: {}, onFailure: () => { error = "Could not disconnect the devbox."; } },
    );
  }

  function select(value: string): void {
    pending = "preference";
    error = "";
    message = "";
    runtime.runCommand(saveWorkspaceSettings({
      baseline: settings.getWorkspaceSettings(), changes: { default_execution_target: value }, store: settings,
    }).pipe(Effect.asVoid, Effect.ensuring(Effect.sync(() => { pending = ""; }))), {
      operation: "choose default workspace host", safeContext: {},
      onFailure: () => { error = "Could not save the workspace host preference."; },
    });
  }
</script>

<section class="devbox-settings" aria-label="Workspace machines">
  <div class="list-heading">
    <div class="copy">
      <h3>Workspace machines</h3>
      <p>Select where new workspaces run. Existing workspaces stay on the machine that created them.</p>
    </div>
    <Button size="sm" disabled={busy} onclick={() => discover()} ariaLabel="Refresh devboxes">
      <RefreshCwIcon size={14} aria-hidden="true" />
      {pending === "discover" ? "Searching…" : "Refresh"}
    </Button>
  </div>
  {#if preferredMissing && !busy}
    <p class="warning" role="alert">Your default machine is no longer connected. Reconnect it or select another machine below.</p>
  {/if}

  <ul class="machine-list" aria-label="Workspace machines">
    <li class="machine-row">
      <input type="radio" name="workspace-machine" value="" bind:group={choice} disabled={busy} aria-label="Run new workspaces on this Forge machine" onchange={() => select("")} />
      <MonitorIcon size={18} aria-hidden="true" />
      <div class="copy machine">
        <div class="machine-heading"><strong>This Forge machine</strong>{#if selfHost?.name.trim()}<span class="state">{selfHost.name.trim()}</span>{/if}</div>
        <p>Files, agents and tests run where Forge is running.</p>
      </div>
    </li>
    {#each connections as connection (connection.id)}
      {@const current = status(connection)}
      <li class="machine-row">
        <input type="radio" name="workspace-machine" value={`devbox:${connection.id}`} bind:group={choice} disabled={busy || connection.maintenance} aria-label={`Run new workspaces on ${connection.name}`} onchange={() => select(`devbox:${connection.id}`)} />
        <ServerIcon size={18} aria-hidden="true" />
        <div class="copy machine">
          <div class="machine-heading">
            <strong>{connection.name}</strong>
            <span class={["state", current.offline && "offline", current.label === "Online" && "online"]}>{current.label}</span>
          </div>
          <p>Devbox · Account {connection.account}{current.detail ? ` · ${current.detail}` : ""}</p>
        </div>
        <div class="actions">
          {#if current.offline}
            <Button size="sm" disabled={busy} onclick={() => reconnect(connection)} ariaLabel={`Reconnect ${connection.name}`}>Reconnect</Button>
          {/if}
          <Menu>
            <MenuTrigger disabled={busy} ariaLabel={`Manage ${connection.name}`} title={`Manage ${connection.name}`}><EllipsisIcon size={16} aria-hidden="true" /></MenuTrigger>
            <MenuContent ariaLabel={`Manage ${connection.name}`}>
              <MenuItem onselect={() => reconnect(connection)}>Reconnect</MenuItem>
              <MenuItem onselect={() => disconnect(connection.id)}>Disconnect</MenuItem>
            </MenuContent>
          </Menu>
        </div>
      </li>
    {/each}
    {#each available as assignment (assignment.host_id)}
      <li class="machine-row">
        <input type="radio" name="workspace-machine" value={`assignment:${assignment.host_id}`} bind:group={choice} disabled aria-label={`Run new workspaces on ${assignment.name}`} title="Connect this devbox first" />
        <ServerIcon size={18} aria-hidden="true" />
        <div class="copy machine">
          <div class="machine-heading"><strong>{assignment.name}</strong><span class="state">{assignment.maintenance ? "Maintenance" : "Not connected"}</span></div>
          <p>Devbox · Account {assignment.account} · Assigned to you by your operator.</p>
        </div>
        <Button size="sm" tone="info" disabled={busy || assignment.maintenance} onclick={() => connect(assignment.host_id)} ariaLabel={`Connect ${assignment.name}`}>
          {pending === `connect:${assignment.host_id}` ? "Connecting…" : "Connect"}
        </Button>
      </li>
    {/each}
  </ul>
  {#if pending === "discover"}<p class="list-message" role="status">Looking for your devboxes…</p>
  {:else if discovery && connections.length === 0 && available.length === 0}
    <p class="list-message">No devboxes are assigned to you yet. Ask your administrator for access, then refresh.</p>
  {/if}

  <div class="registry">
    {#if discovery?.registry_url && !registryOpen}
      <p>Devboxes come from <code>{discovery.registry_url}</code>. <button type="button" class="link" disabled={busy} onclick={() => { registry = discovery?.registry_url ?? ""; registryOpen = true; }}>Change registry</button></p>
    {:else}
      {#if discoveryError}
        <p role="alert">Could not discover devboxes. {discoveryError}{connections.length > 0 ? " Connected devboxes are still listed." : ""}</p>
      {/if}
      <form onsubmit={(event) => { event.preventDefault(); discover(); }}>
        <label for="devbox-registry">Registry address</label>
        <div class="registry-input">
          <TextInput block id="devbox-registry" bind:value={registry} disabled={busy} placeholder="https://" />
          <Button size="sm" type="submit" disabled={busy}>Find devboxes</Button>
          {#if discovery?.registry_url}<Button size="sm" type="button" disabled={busy} onclick={() => { registryOpen = false; }}>Cancel</Button>{/if}
        </div>
        <p>Your administrator provides this address. It lists the machines assigned to you; you never enter ports or tokens.</p>
      </form>
    {/if}
  </div>
  {#if message}<p role="status">{message}</p>{/if}
  {#if error}<p role="alert">{error}</p>{/if}
</section>

<style>
  .devbox-settings { display: grid; gap: var(--space-4); padding-bottom: var(--space-6); margin-bottom: var(--space-6); border-bottom: 1px solid var(--border-muted); }
  .list-heading, .machine-row, .actions, .machine-heading, .registry-input { display: flex; align-items: center; gap: var(--space-4); }
  .list-heading { justify-content: space-between; }
  .copy { display: grid; gap: var(--space-2); min-width: 0; }
  h3 { margin: 0; color: var(--text-primary); font-size: var(--font-size-md); font-weight: var(--font-weight-semibold); }
  p { color: var(--text-muted); font-size: var(--font-size-sm); line-height: 1.5; margin: 0; }
  .machine-list { list-style: none; margin: 0; padding: 0; border: 1px solid var(--border-muted); border-radius: var(--radius-md); overflow: hidden; }
  .machine-row { padding: var(--space-4) var(--space-5); color: var(--text-muted); }
  .machine-row + .machine-row { border-top: 1px solid var(--border-muted); }
  input[type="radio"] { margin: 0; width: 16px; height: 16px; flex: none; accent-color: var(--accent-blue); cursor: pointer; }
  input[type="radio"]:disabled { cursor: not-allowed; }
  .machine { flex: 1; }
  .machine-heading { flex-wrap: wrap; }
  strong { color: var(--text-primary); font-size: var(--font-size-md); font-weight: var(--font-weight-medium); overflow-wrap: anywhere; }
  .state { font-size: var(--font-size-xs); color: var(--text-secondary); background: var(--bg-inset); border-radius: var(--radius-sm); padding: var(--space-1) var(--space-3); }
  .state.online { color: var(--accent-green); }
  .state.offline { color: var(--accent-red); }
  .list-message { padding: 0 var(--space-5); }
  .registry { display: grid; gap: var(--space-3); font-size: var(--font-size-sm); color: var(--text-secondary); }
  code { font-size: var(--font-size-sm); overflow-wrap: anywhere; }
  .link { background: none; border: 0; padding: 0; color: var(--accent-blue); font: inherit; cursor: pointer; }
  .link:disabled { cursor: default; opacity: var(--opacity-disabled); }
  form { display: grid; gap: var(--space-3); }
  .registry-input > :global(:first-child) { flex: 1; min-width: 0; }
  .actions { flex-wrap: wrap; }
  [role="alert"], .warning { color: var(--accent-red); }
  @media (max-width: 640px) {
    .machine-row { flex-wrap: wrap; }
    .actions { margin-left: auto; justify-content: flex-end; }
    .registry-input { flex-wrap: wrap; }
  }
</style>
