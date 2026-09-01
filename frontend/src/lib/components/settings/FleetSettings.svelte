<script lang="ts">
  import { Button, Card, Checkbox, EmptyState } from "@kenn-io/kit-ui";
  import RotateCcwIcon from "@lucide/svelte/icons/rotate-ccw";
  import { Effect } from "effect";
  import type {
    FleetSettings as FleetSettingsType,
    FleetSettingsUpdate,
  } from "../../api/types.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { isEmbedded } from "../../stores/embed-config.svelte.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import {
    SettingsWorkflow,
    settingsErrorMessage,
  } from "../../stores/settings-workflow.js";

  interface Props {
    fleet: FleetSettingsType;
    onUpdate: (fleet: FleetSettingsType) => void;
  }

  let { fleet, onUpdate }: Props = $props();

  const runtime = getAppRuntime();
  const embedded = isEmbedded();
  // svelte-ignore state_referenced_locally
  let currentFleet = $state(fleet);
  let saving = $state(false);
  // svelte-ignore state_referenced_locally
  let enabledDraft = $state(currentFleet.enabled);
  // svelte-ignore state_referenced_locally
  let peerTimeoutDraft = $state(currentFleet.peer_timeout ?? "");
  // svelte-ignore state_referenced_locally
  let includeUnmanagedDetailsDraft = $state(
    currentFleet.sessions.include_unmanaged_details ?? false,
  );

  const pendingFleet = $derived(buildPendingFleet());
  const savedFleet = $derived(normalizeFleetForCompare(currentFleet));
  const isDirty = $derived(
    JSON.stringify(pendingFleet) !== JSON.stringify(savedFleet),
  );
  const canSave = $derived(!embedded && !saving && isDirty);
  const isHub = $derived(currentFleet.role === "hub");

  function buildPendingFleet(): FleetSettingsUpdate {
    return {
      enabled: enabledDraft,
      peer_timeout: peerTimeoutDraft.trim(),
      sessions: {
        include_unmanaged_details: includeUnmanagedDetailsDraft,
      },
    };
  }

  function normalizeFleetForCompare(
    value: FleetSettingsType,
  ): FleetSettingsUpdate {
    return {
      enabled: value.enabled,
      peer_timeout: value.peer_timeout ?? "",
      sessions: {
        include_unmanaged_details:
          value.sessions.include_unmanaged_details ?? false,
      },
    };
  }

  function resetDraft(): void {
    enabledDraft = currentFleet.enabled;
    peerTimeoutDraft = currentFleet.peer_timeout ?? "";
    includeUnmanagedDetailsDraft =
      currentFleet.sessions.include_unmanaged_details ?? false;
  }

  function displayName(name: string | undefined, fallback: string): string {
    const trimmed = name?.trim() ?? "";
    return trimmed === "" ? fallback : trimmed;
  }

  function save(): void {
    if (!canSave) return;
    saving = true;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* SettingsWorkflow;
        return yield* workflow.updateFleet(pendingFleet);
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              showFlash(settingsErrorMessage(failure), { tone: "danger" });
            }),
          onSuccess: (updatedFleet) =>
            Effect.sync(() => {
              currentFleet = updatedFleet;
              resetDraft();
              onUpdate(updatedFleet);
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            saving = false;
          }),
        ),
      ),
      {
        operation: "save fleet settings",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }
</script>

<div class="fleet-settings">
  <Card level="inset" padding="sm" class="role-card">
    <div>
      <span class="eyebrow">This Forge</span>
      <strong>{isHub ? "Federation hub" : "Federation spoke"}</strong>
    </div>
    <span class:enabled={currentFleet.enabled} class="state-badge">
      {currentFleet.enabled ? "Enabled" : "Disabled"}
    </span>
  </Card>

  <Checkbox
    class="toggle-row"
    bind:checked={enabledDraft}
    disabled={embedded || saving}
    ariaLabel="Enable fleet federation"
  >
    <span>
      <span class="field-label">Enable fleet federation</span>
      <span class="field-help">
        {isHub
          ? "The hub can reach active members while federation is enabled."
          : "This spoke can use its hub binding while federation is enabled."}
      </span>
    </span>
  </Checkbox>

  {#if currentFleet.restart_required}
    <p class="restart-banner">Restart required</p>
  {/if}

  <div class="settings-grid">
    <label class="field">
      <span class="field-label">Member request timeout</span>
      <input
        value={peerTimeoutDraft}
        oninput={(event) => {
          peerTimeoutDraft = event.currentTarget instanceof HTMLInputElement
            ? event.currentTarget.value
            : "";
        }}
        placeholder="2s"
        disabled={embedded || saving}
        aria-label="Member request timeout"
      />
      <span class="field-help">Bounds snapshot and health requests to another Forge.</span>
    </label>
  </div>

  <Checkbox
    class="check-row"
    bind:checked={includeUnmanagedDetailsDraft}
    disabled={embedded || saving}
    ariaLabel="Include unmanaged tmux details"
  >
    <span>
      <span class="field-label">Include unmanaged tmux details</span>
      <span class="field-help">Changing this monitor setting applies after restart.</span>
    </span>
  </Checkbox>

  {#if isHub}
    <section class="membership-section" aria-label="Federation members">
      <div class="section-heading">
        <div>
          <h3>Enrolled spokes</h3>
          <p>Membership is managed by the one-time enrollment workflow.</p>
        </div>
        <span class="count">{currentFleet.members.length}</span>
      </div>

      {#if currentFleet.members.length === 0}
        <EmptyState title="No active spokes are enrolled." />
      {:else}
        <div class="table-wrap">
          <table aria-label="Federation member status">
            <thead>
              <tr>
                <th scope="col">Spoke</th>
                <th scope="col">HTTPS endpoint</th>
                <th scope="col">State</th>
              </tr>
            </thead>
            <tbody>
              {#each currentFleet.members as member (member.node_id)}
                <tr>
                  <td>
                    <strong>{displayName(member.name, "Unnamed spoke")}</strong>
                    <code>{member.node_id}</code>
                  </td>
                  <td><a class="endpoint-link" href={member.base_url}>{member.base_url}</a></td>
                  <td><span class="state-badge enabled">{member.state}</span></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    {#if currentFleet.enrollments.length > 0}
      <section class="membership-section" aria-label="Enrollment activity">
        <div class="section-heading">
          <div>
            <h3>Enrollment activity</h3>
            <p>Prepared, pending, and revoked enrollment records.</p>
          </div>
          <span class="count">{currentFleet.enrollments.length}</span>
        </div>
        <ul class="enrollment-list">
          {#each currentFleet.enrollments as enrollment (enrollment.id)}
            <li>
              <span>
                <strong>{displayName(enrollment.spoke_name, enrollment.node_id)}</strong>
                <code>{enrollment.spoke_base_url}</code>
              </span>
              <span class="state-badge">{enrollment.state}</span>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {:else}
    <section class="membership-section" aria-label="Hub binding">
      <div class="section-heading">
        <div>
          <h3>Hub</h3>
          <p>This binding is written by enrollment and changes after restart.</p>
        </div>
      </div>
      {#if currentFleet.hub}
        <dl class="binding">
          <div>
            <dt>Name</dt>
            <dd>{displayName(currentFleet.hub.name, "Unnamed hub")}</dd>
          </div>
          <div>
            <dt>Node ID</dt>
            <dd><code>{currentFleet.hub.node_id}</code></dd>
          </div>
          <div>
            <dt>HTTPS endpoint</dt>
            <dd>
              <a class="endpoint-link" href={currentFleet.hub.base_url}>
                {currentFleet.hub.base_url}
              </a>
            </dd>
          </div>
        </dl>
      {:else}
        <EmptyState title="This spoke has not enrolled with a hub." />
      {/if}
    </section>
  {/if}

  <div class="settings-actions">
    <Button
      size="sm"
      type="button"
      onclick={resetDraft}
      disabled={!isDirty || saving}
    >
      <RotateCcwIcon size="14" strokeWidth="2.2" aria-hidden="true" />
      Reset
    </Button>
    <Button
      tone="info"
      surface="solid"
      type="button"
      onclick={save}
      disabled={!canSave}
    >
      Save fleet federation
    </Button>
  </div>
</div>

<style>
  .fleet-settings {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  :global(.role-card),
  .section-heading,
  .enrollment-list li {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
  }

  :global(.role-card strong),
  .field-label,
  .section-heading h3,
  .enrollment-list strong {
    display: block;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 700;
  }

  .eyebrow {
    display: block;
    margin-bottom: 2px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .state-badge,
  .count {
    flex: 0 0 auto;
    padding: 2px 7px;
    border: 1px solid var(--border-default);
    border-radius: 999px;
    color: var(--text-secondary);
    background: var(--bg-primary);
    font-size: var(--font-size-xs);
  }

  .state-badge.enabled {
    border-color: var(--status-success-border, var(--border-default));
    color: var(--status-success-text, var(--text-primary));
    background: var(--status-success-bg, var(--bg-primary));
  }

  :global(.toggle-row) {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
  }

  :global(.toggle-row .kit-checkbox__box) {
    order: 2;
    margin-top: 2px;
  }

  :global(.check-row) {
    align-items: flex-start;
  }

  :global(.check-row .kit-checkbox__box) {
    margin-top: 2px;
  }

  .field-help,
  .section-heading p {
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

  .field input {
    width: 100%;
    min-height: 30px;
    padding: 4px 8px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-primary);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .field input:disabled {
    color: var(--text-muted);
    background: var(--bg-inset);
  }

  .restart-banner {
    margin: 0;
    padding: 8px 10px;
    border: 1px solid var(--diff-stale-border);
    border-radius: var(--radius-sm);
    color: var(--diff-stale-text);
    background: var(--diff-stale-bg);
    font-size: var(--font-size-sm);
  }

  .membership-section {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    padding-top: 12px;
    border-top: 1px solid var(--border-muted);
  }

  .section-heading h3,
  .section-heading p {
    margin: 0;
  }

  .section-heading p {
    margin-top: 2px;
  }

  .table-wrap {
    overflow-x: auto;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
  }

  table {
    width: 100%;
    min-width: 600px;
    border-collapse: collapse;
  }

  th,
  td {
    padding: 8px;
    border-bottom: 1px solid var(--border-muted);
    vertical-align: top;
    text-align: left;
  }

  tbody tr:last-child td {
    border-bottom: 0;
  }

  th {
    color: var(--text-secondary);
    background: var(--bg-inset);
    font-size: var(--font-size-xs);
    font-weight: 700;
  }

  td strong,
  td code,
  .enrollment-list code {
    display: block;
  }

  code {
    overflow-wrap: anywhere;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }

  .endpoint-link {
    display: block;
    overflow-wrap: anywhere;
    color: var(--accent-blue);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
  }

  .enrollment-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .enrollment-list li {
    padding: 9px 10px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
  }

  .binding {
    display: grid;
    gap: 8px;
    margin: 0;
  }

  .binding div {
    display: grid;
    grid-template-columns: minmax(100px, 0.35fr) minmax(0, 1fr);
    gap: 12px;
  }

  .binding dt {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .binding dd {
    min-width: 0;
    margin: 0;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .settings-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
