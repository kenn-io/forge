<script lang="ts">
  import PlusIcon from "@lucide/svelte/icons/plus";
  import RotateCcwIcon from "@lucide/svelte/icons/rotate-ccw";
  import TrashIcon from "@lucide/svelte/icons/trash-2";
  import { ActionButton } from "@middleman/ui";
  import type {
    FleetPeer,
    FleetSSHPeer,
    FleetSettings as FleetSettingsType,
    FleetSettingsUpdate,
  } from "@middleman/ui/api/types";
  import { updateFleetSettings } from "../../api/settings.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  import TypeaheadTrigger, { type TypeaheadOption } from "../shared/TypeaheadTrigger.svelte";

  interface Props {
    fleet: FleetSettingsType;
    onUpdate: (fleet: FleetSettingsType) => void;
  }

  interface HTTPPeerDraft {
    id: string;
    key: string;
    name: string;
    baseURL: string;
  }

  interface SSHPeerDraft {
    id: string;
    key: string;
    name: string;
    destination: string;
    platform: string;
    remoteCommand: string;
  }

  let { fleet, onUpdate }: Props = $props();

  const embedded = isEmbedded();
  const basePlatformOptions: TypeaheadOption[] = [
    { value: "macos", label: "macOS" },
    { value: "linux", label: "linux" },
    { value: "windows", label: "windows" },
  ];
  // svelte-ignore state_referenced_locally
  let currentFleet = $state(fleet);
  let nextID = 0;
  let saving = $state(false);
  let error = $state<string | null>(null);
  // svelte-ignore state_referenced_locally
  let enabledDraft = $state(currentFleet.enabled);
  // svelte-ignore state_referenced_locally
  let keyDraft = $state(currentFleet.key ?? "");
  // svelte-ignore state_referenced_locally
  let peerTimeoutDraft = $state(currentFleet.peer_timeout ?? "");
  // svelte-ignore state_referenced_locally
  let includeUnmanagedDetailsDraft = $state(
    currentFleet.sessions.include_unmanaged_details ?? false,
  );
  // svelte-ignore state_referenced_locally
  let httpPeerDrafts = $state<HTTPPeerDraft[]>(httpDraftsFromFleet(currentFleet));
  // svelte-ignore state_referenced_locally
  let sshPeerDrafts = $state<SSHPeerDraft[]>(sshDraftsFromFleet(currentFleet));

  const pendingFleet = $derived(buildPendingFleet());
  const savedFleet = $derived(normalizeFleetForCompare(currentFleet));
  const isDirty = $derived(
    JSON.stringify(pendingFleet) !== JSON.stringify(savedFleet),
  );
  const hasInvalidDraft = $derived(
    httpPeerDrafts.some((peer) => peer.key.trim() === "" || peer.baseURL.trim() === "") ||
      sshPeerDrafts.some((peer) => peer.key.trim() === "" || peer.destination.trim() === ""),
  );
  const canSave = $derived(!embedded && !saving && isDirty && !hasInvalidDraft);

  function nextDraftID(prefix: string): string {
    nextID += 1;
    return `${prefix}:${nextID}`;
  }

  function httpDraftsFromFleet(value: FleetSettingsType): HTTPPeerDraft[] {
    return value.peers.map((peer, index) => ({
      id: `http:${peer.key || index}:${nextID++}`,
      key: peer.key,
      name: peer.name ?? "",
      baseURL: peer.base_url,
    }));
  }

  function sshDraftsFromFleet(value: FleetSettingsType): SSHPeerDraft[] {
    return value.ssh_peers.map((peer, index) => ({
      id: `ssh:${peer.key || index}:${nextID++}`,
      key: peer.key,
      name: peer.name ?? "",
      destination: peer.destination,
      platform: peer.platform ?? "",
      remoteCommand: peer.remote_command ?? "",
    }));
  }

  function compactHTTPPeer(peer: HTTPPeerDraft): FleetPeer {
    const out: FleetPeer = {
      key: peer.key.trim(),
      base_url: peer.baseURL.trim(),
    };
    const name = peer.name.trim();
    if (name !== "") out.name = name;
    return out;
  }

  function compactSSHPeer(peer: SSHPeerDraft): FleetSSHPeer {
    const out: FleetSSHPeer = {
      key: peer.key.trim(),
      destination: peer.destination.trim(),
    };
    const name = peer.name.trim();
    const platform = peer.platform.trim();
    const remoteCommand = peer.remoteCommand.trim();
    if (name !== "") out.name = name;
    if (platform !== "") out.platform = platform;
    if (remoteCommand !== "") out.remote_command = remoteCommand;
    return out;
  }

  function buildPendingFleet(): FleetSettingsUpdate {
    return {
      enabled: enabledDraft,
      key: keyDraft.trim(),
      peer_timeout: peerTimeoutDraft.trim(),
      sessions: {
        include_unmanaged_details: includeUnmanagedDetailsDraft,
      },
      peers: httpPeerDrafts.map(compactHTTPPeer),
      ssh_peers: sshPeerDrafts.map(compactSSHPeer),
    };
  }

  function normalizeFleetForCompare(value: FleetSettingsType): FleetSettingsUpdate {
    return {
      enabled: value.enabled,
      key: value.key ?? "",
      peer_timeout: value.peer_timeout ?? "",
      sessions: {
        include_unmanaged_details:
          value.sessions.include_unmanaged_details ?? false,
      },
      peers: value.peers.map((peer) => compactHTTPPeer({
        id: "",
        key: peer.key,
        name: peer.name ?? "",
        baseURL: peer.base_url,
      })),
      ssh_peers: value.ssh_peers.map((peer) => compactSSHPeer({
        id: "",
        key: peer.key,
        name: peer.name ?? "",
        destination: peer.destination,
        platform: peer.platform ?? "",
        remoteCommand: peer.remote_command ?? "",
      })),
    };
  }

  function resetDraft(): void {
    enabledDraft = currentFleet.enabled;
    keyDraft = currentFleet.key ?? "";
    peerTimeoutDraft = currentFleet.peer_timeout ?? "";
    includeUnmanagedDetailsDraft =
      currentFleet.sessions.include_unmanaged_details ?? false;
    httpPeerDrafts = httpDraftsFromFleet(currentFleet);
    sshPeerDrafts = sshDraftsFromFleet(currentFleet);
    error = null;
  }

  function addHTTPPeer(): void {
    httpPeerDrafts = [
      ...httpPeerDrafts,
      { id: nextDraftID("http"), key: "", name: "", baseURL: "" },
    ];
  }

  function removeHTTPPeer(id: string): void {
    httpPeerDrafts = httpPeerDrafts.filter((peer) => peer.id !== id);
  }

  function addSSHPeer(): void {
    sshPeerDrafts = [
      ...sshPeerDrafts,
      {
        id: nextDraftID("ssh"),
        key: "",
        name: "",
        destination: "",
        platform: "",
        remoteCommand: "",
      },
    ];
  }

  function removeSSHPeer(id: string): void {
    sshPeerDrafts = sshPeerDrafts.filter((peer) => peer.id !== id);
  }

  function peerLabel(prefix: string, key: string, index: number): string {
    const trimmed = key.trim();
    return trimmed === "" ? `${prefix} ${index + 1}` : trimmed;
  }

  function platformOptions(value: string): TypeaheadOption[] {
    const trimmed = value.trim();
    if (trimmed === "" || basePlatformOptions.some((option) => option.value === trimmed)) {
      return basePlatformOptions;
    }
    return [...basePlatformOptions, { value: trimmed, label: trimmed }];
  }

  async function save(): Promise<void> {
    if (!canSave) return;
    saving = true;
    error = null;
    try {
      const updated = await updateFleetSettings(pendingFleet);
      currentFleet = updated;
      resetDraft();
      onUpdate(updated);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }
</script>

<div class="fleet-settings">
  <label class="toggle-row">
    <span>
      <span class="field-label">Enable fleet federation</span>
      <span class="field-help">
        Remote hosts stay unavailable while federation is off.
      </span>
    </span>
    <input
      type="checkbox"
      bind:checked={enabledDraft}
      disabled={embedded || saving}
      aria-label="Enable fleet federation"
    />
  </label>

  {#if currentFleet.restart_required}
    <p class="restart-banner">Restart required</p>
  {/if}

  {#if error}
    <p class="settings-error">{error}</p>
  {/if}

  <div class="settings-grid">
    <label class="field">
      <span class="field-label">Local fleet key</span>
      <input
        value={keyDraft}
        oninput={(event) => {
          keyDraft = event.currentTarget instanceof HTMLInputElement
            ? event.currentTarget.value
            : "";
        }}
        placeholder="Optional stable hub key"
        disabled={embedded || saving}
        aria-label="Local fleet key"
      />
      <span class="field-help">Hubs should use a stable key; empty keeps the current hostname fallback.</span>
    </label>

    <label class="field">
      <span class="field-label">Peer timeout</span>
      <input
        value={peerTimeoutDraft}
        oninput={(event) => {
          peerTimeoutDraft = event.currentTarget instanceof HTMLInputElement
            ? event.currentTarget.value
            : "";
        }}
        placeholder="2s"
        disabled={embedded || saving}
        aria-label="Peer timeout"
      />
    </label>
  </div>

  <label class="check-row">
    <input
      type="checkbox"
      bind:checked={includeUnmanagedDetailsDraft}
      disabled={embedded || saving}
    />
    <span>
      <span class="field-label">Include unmanaged tmux details</span>
      <span class="field-help">Changing this monitor setting applies after restart.</span>
    </span>
  </label>

  <section class="peer-section" aria-label="HTTP fleet peers">
    <div class="peer-section-header">
      <div>
        <h3>HTTP peers</h3>
        <p>Use only on a trusted network boundary; hub credentials are not forwarded.</p>
      </div>
      <ActionButton
        size="sm"
        type="button"
        onclick={addHTTPPeer}
        disabled={embedded || saving}
      >
        <PlusIcon size="14" strokeWidth="2.2" aria-hidden="true" />
        Add HTTP peer
      </ActionButton>
    </div>

    {#if httpPeerDrafts.length === 0}
      <p class="empty-peers">No HTTP peers configured.</p>
    {:else}
      <div class="peer-table-wrap">
        <table class="peer-table http" aria-label="HTTP peer membership">
          <colgroup>
            <col class="http-key-col" />
            <col class="http-name-col" />
            <col class="http-url-col" />
            <col class="peer-action-col" />
          </colgroup>
          <thead>
            <tr>
              <th scope="col">Key</th>
              <th scope="col">Name</th>
              <th scope="col">Base URL</th>
              <th scope="col" aria-label="HTTP peer actions"></th>
            </tr>
          </thead>
          <tbody>
            {#each httpPeerDrafts as peer, index (peer.id)}
              {@const label = peerLabel("HTTP peer", peer.key, index)}
              <tr>
                <td>
                  <input
                    bind:value={peer.key}
                    disabled={embedded || saving}
                    aria-label={`HTTP peer ${label} key`}
                  />
                </td>
                <td>
                  <input
                    bind:value={peer.name}
                    disabled={embedded || saving}
                    aria-label={`HTTP peer ${label} name`}
                  />
                </td>
                <td>
                  <input
                    bind:value={peer.baseURL}
                    disabled={embedded || saving}
                    aria-label={`HTTP peer ${label} base URL`}
                  />
                </td>
                <td class="action-cell">
                  <ActionButton
                    size="sm"
                    tone="danger"
                    surface="outline"
                    type="button"
                    onclick={() => removeHTTPPeer(peer.id)}
                    disabled={embedded || saving}
                    ariaLabel={`Remove HTTP peer ${label}`}
                    title={`Remove HTTP peer ${label}`}
                  >
                    <TrashIcon size="14" strokeWidth="2.2" aria-hidden="true" />
                  </ActionButton>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <section class="peer-section" aria-label="SSH fleet peers">
    <div class="peer-section-header">
      <div>
        <h3>SSH peers</h3>
        <p>Private relay members reached by running the peer CLI remotely.</p>
      </div>
      <ActionButton
        size="sm"
        type="button"
        onclick={addSSHPeer}
        disabled={embedded || saving}
      >
        <PlusIcon size="14" strokeWidth="2.2" aria-hidden="true" />
        Add SSH peer
      </ActionButton>
    </div>

    {#if sshPeerDrafts.length === 0}
      <p class="empty-peers">No SSH peers configured.</p>
    {:else}
      <div class="peer-table-wrap">
        <table class="peer-table ssh" aria-label="SSH peer membership">
          <colgroup>
            <col class="ssh-key-col" />
            <col class="ssh-name-col" />
            <col class="ssh-destination-col" />
            <col class="ssh-platform-col" />
            <col class="ssh-command-col" />
            <col class="peer-action-col" />
          </colgroup>
          <thead>
            <tr>
              <th scope="col">Key</th>
              <th scope="col">Name</th>
              <th scope="col">Destination</th>
              <th scope="col">Platform</th>
              <th scope="col">Remote command</th>
              <th scope="col" aria-label="SSH peer actions"></th>
            </tr>
          </thead>
          <tbody>
            {#each sshPeerDrafts as peer, index (peer.id)}
              {@const label = peerLabel("SSH peer", peer.key, index)}
              <tr>
                <td>
                  <input
                    bind:value={peer.key}
                    disabled={embedded || saving}
                    aria-label={`SSH peer ${label} key`}
                  />
                </td>
                <td>
                  <input
                    bind:value={peer.name}
                    disabled={embedded || saving}
                    aria-label={`SSH peer ${label} name`}
                  />
                </td>
                <td>
                  <input
                    bind:value={peer.destination}
                    disabled={embedded || saving}
                    aria-label={`SSH peer ${label} destination`}
                  />
                </td>
                <td class="platform-cell">
                  <TypeaheadTrigger
                    ariaLabel={`SSH peer ${label} platform`}
                    selected={peer.platform.trim() === "" ? null : peer.platform}
                    options={platformOptions(peer.platform)}
                    allowClear
                    allowCustom
                    clearLabel="Unspecified"
                    placeholder="Platform..."
                    emptyLabel="Enter a platform"
                    placement="top"
                    triggerAriaLabel={`SSH peer ${label} platform: ${
                      platformOptions(peer.platform).find((option) => option.value === peer.platform)?.label ??
                        "Unspecified"
                    }`}
                    onChange={(value) => {
                      peer.platform = value ?? "";
                    }}
                    disabled={embedded || saving}
                  />
                </td>
                <td>
                  <input
                    bind:value={peer.remoteCommand}
                    placeholder="middleman"
                    disabled={embedded || saving}
                    aria-label={`SSH peer ${label} remote command`}
                  />
                </td>
                <td class="action-cell">
                  <ActionButton
                    size="sm"
                    tone="danger"
                    surface="outline"
                    type="button"
                    onclick={() => removeSSHPeer(peer.id)}
                    disabled={embedded || saving}
                    ariaLabel={`Remove SSH peer ${label}`}
                    title={`Remove SSH peer ${label}`}
                  >
                    <TrashIcon size="14" strokeWidth="2.2" aria-hidden="true" />
                  </ActionButton>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <div class="settings-actions">
    <ActionButton
      size="sm"
      type="button"
      onclick={resetDraft}
      disabled={!isDirty || saving}
    >
      <RotateCcwIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      Reset
    </ActionButton>
    <ActionButton
      tone="info"
      surface="solid"
      type="button"
      onclick={() => void save()}
      disabled={!canSave}
    >
      Save fleet federation
    </ActionButton>
  </div>
</div>

<style>
  .fleet-settings {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .toggle-row,
  .check-row {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 16px;
  }

  .check-row {
    justify-content: flex-start;
  }

  .toggle-row input,
  .check-row input {
    margin-top: 2px;
  }

  .field-label {
    display: block;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 600;
  }

  .field-help,
  .peer-section-header p,
  .empty-peers {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    line-height: 1.4;
  }

  .settings-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }

  .field input,
  .peer-table input,
  .platform-cell :global(.typeahead-trigger),
  .platform-cell :global(.typeahead-input) {
    width: 100%;
    min-height: 30px;
    padding: 4px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 400;
  }

  .field input:disabled,
  .peer-table input:disabled,
  .platform-cell :global(.typeahead-trigger:disabled) {
    color: var(--text-muted);
    background: var(--bg-inset);
  }

  .platform-cell :global(.typeahead) {
    width: 100%;
    min-width: 0;
  }

  .platform-cell :global(.typeahead-trigger),
  .platform-cell :global(.typeahead-input) {
    height: 30px;
  }

  .restart-banner,
  .settings-error {
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    font-size: var(--font-size-sm);
  }

  .restart-banner {
    border: 1px solid var(--diff-stale-border);
    background: var(--diff-stale-bg);
    color: var(--diff-stale-text);
  }

  .settings-error {
    border: 1px solid color-mix(in srgb, var(--accent-red) 45%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-red) 9%, var(--bg-primary));
    color: var(--accent-red);
  }

  .peer-section {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding-top: 12px;
    border-top: 1px solid var(--border-muted);
  }

  .peer-section-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
  }

  .peer-section-header h3 {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 700;
  }

  .peer-section-header p {
    margin: 2px 0 0;
  }

  .peer-table-wrap {
    overflow: visible;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
  }

  .peer-table {
    width: 100%;
    min-width: 620px;
    border-collapse: collapse;
    table-layout: fixed;
  }

  .peer-table.ssh {
    min-width: 680px;
  }

  .peer-table th,
  .peer-table td {
    padding: 8px;
    border-bottom: 1px solid var(--border-muted);
    vertical-align: top;
  }

  .peer-table tbody tr:last-child td {
    border-bottom: 0;
  }

  .peer-table th {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 700;
    text-align: left;
    background: var(--bg-inset);
  }

  .http-key-col {
    width: 20%;
  }

  .http-name-col {
    width: 26%;
  }

  .http-url-col,
  .ssh-command-col {
    width: auto;
  }

  .ssh-key-col {
    width: 14%;
  }

  .ssh-name-col {
    width: 18%;
  }

  .ssh-destination-col {
    width: 24%;
  }

  .ssh-platform-col {
    width: 15%;
  }

  .peer-action-col {
    width: 46px;
  }

  .action-cell {
    text-align: right;
  }

  .settings-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }

  @media (min-width: 760px) {
    .settings-grid {
      grid-template-columns: minmax(0, 1fr) minmax(160px, 0.5fr);
    }
  }

  @media (max-width: 759px) {
    .peer-table-wrap {
      overflow-x: auto;
    }
  }
</style>
