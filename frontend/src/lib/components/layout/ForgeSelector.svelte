<script lang="ts">
  import { Effect } from "effect";
  import { dismissable, StatusDot, type StatusDotStatus } from "@kenn-io/kit-ui";
  import { untrack } from "svelte";
  import { ChevronDownIcon } from "../../icons.ts";
  import {
    loadSnapshotHosts,
    type HostSummary,
  } from "../../api/fleet-snapshot.js";
  import {
    fleetBrowserLoginFailureMessage,
    resolveFleetHostDestination,
  } from "../../api/fleet-browser-login.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { currentInAppPath, navigateToURL } from "../../utils/pageNavigation.js";

  interface Props {
    compact?: boolean;
    fallbackLabel?: string;
  }

  let { compact = false, fallbackLabel = "" }: Props = $props();
  const runtime = getAppRuntime();
  let hosts = $state.raw<HostSummary[]>([]);
  let loadExecution: AppExecution<void, never> | undefined;
  let selectorEl = $state<HTMLDetailsElement>();
  let selectorOpen = $state(false);
  // The host whose sign-in link is being requested or opened. It stays set
  // after navigation starts so a second click cannot mint another ticket.
  let switchingNodeID = $state<string | null>(null);

  const directoryHosts = $derived(hosts.filter((host) => host.kind === "self" || host.kind === "devbox" || Boolean(host.baseURL)));
  const orderedHosts = $derived.by(() =>
    [...directoryHosts].sort((left, right) =>
      Number(right.federationRole === "hub") - Number(left.federationRole === "hub") ||
      hostName(left).localeCompare(hostName(right)),
    ),
  );
  const currentHost = $derived(
    directoryHosts.find((host) => host.kind === "self"),
  );
  const showSelector = $derived(directoryHosts.length > 1 && currentHost !== undefined);

  function hostName(host: HostSummary): string {
    return host.name.trim() || host.nodeID;
  }

  function hostStatus(host: HostSummary): "online" | "degraded" | "offline" {
    if (!host.reachable || host.connectionState === "offline") return "offline";
    if (host.error || host.connectionState === "degraded") return "degraded";
    return "online";
  }

  function statusDot(status: "online" | "degraded" | "offline"): StatusDotStatus {
    if (status === "online") return "idle";
    if (status === "degraded") return "stale";
    return "unclean";
  }

  function hostDiagnostic(host: HostSummary): string {
    return host.error ?? host.diagnostics?.[0]?.summary ?? "";
  }

  function refresh(): void {
    loadExecution?.interrupt();
    loadExecution = runtime.runCommand(
      loadSnapshotHosts().pipe(
        Effect.matchEffect({
          onFailure: () => Effect.void,
          onSuccess: (nextHosts) => Effect.sync(() => {
            hosts = nextHosts;
          }),
        }),
      ),
      {
        operation: "load Forge fleet directory",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  function isPlainPrimaryClick(event: MouseEvent): boolean {
    return event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
  }

  // Plain clicks sign the browser into the destination Forge before opening
  // it; modified and middle clicks keep the ordinary link behavior.
  function handleHostClick(event: MouseEvent, host: HostSummary): void {
    if (host.kind === "self" || !host.baseURL || event.defaultPrevented || !isPlainPrimaryClick(event)) return;
    event.preventDefault();
    if (switchingNodeID !== null) return;
    switchingNodeID = host.nodeID;
    runtime.runCommand(
      resolveFleetHostDestination({ nodeID: host.nodeID, baseURL: host.baseURL }, currentInAppPath()).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              switchingNodeID = null;
              showFlash(fleetBrowserLoginFailureMessage(failure), { tone: "danger" });
            }),
          onSuccess: (url) => Effect.sync(() => navigateToURL(url)),
        }),
      ),
      {
        operation: "switch Forge fleet host",
        safeContext: {},
        onFailure: () => {},
      },
    );
  }

  // A page restored from the back/forward cache must accept clicks again.
  function handlePageShow(event: PageTransitionEvent): void {
    if (event.persisted) switchingNodeID = null;
  }

  function handleToggle(event: Event): void {
    if (event.currentTarget instanceof HTMLDetailsElement && event.currentTarget.open) {
      refresh();
    }
  }

  function handleVisibilityChange(): void {
    if (document.visibilityState === "visible") refresh();
  }

  $effect(() => {
    untrack(refresh);
    return () => {
      loadExecution?.interrupt();
      loadExecution = undefined;
    };
  });

  $effect(() => {
    if (!selectorOpen) return;
    return dismissable({
      owners: () => [selectorEl],
      dismiss: () => {
        selectorOpen = false;
      },
      escapeFocus: () => selectorEl?.querySelector("summary"),
    });
  });
</script>

<svelte:window onfocus={refresh} onpageshow={handlePageShow} />
<svelte:document onvisibilitychange={handleVisibilityChange} />

{#snippet hostRowContent(host: HostSummary, status: "online" | "degraded" | "offline", diagnostic: string)}
  <StatusDot status={statusDot(status)} label={`${hostName(host)} is ${status}`} size={9} animated />
  <span class="host-copy">
    <span class="host-heading">
      <strong>{hostName(host)}</strong>
      {#if host.kind === "self"}<span class="host-label">Current</span>{/if}
      <span class="host-label">
        {host.kind === "devbox" ? "Devbox" : host.federationRole === "hub" ? "Hub" : "Spoke"}
      </span>
    </span>
    <span class="host-status">{status}</span>
    {#if diagnostic}<span class="host-diagnostic">{diagnostic}</span>{/if}
  </span>
{/snippet}

{#if showSelector && currentHost}
  <details
    bind:this={selectorEl}
    bind:open={selectorOpen}
    class:compact
    class="forge-selector"
    ontoggle={handleToggle}
  >
    <summary aria-label={`Current Forge: ${hostName(currentHost)}`}>
      <StatusDot
        animated
        status={statusDot(hostStatus(currentHost))}
        label={`${hostName(currentHost)} is ${hostStatus(currentHost)}`}
        size={9}
      />
      <span class="current-name">{hostName(currentHost)}</span>
      <ChevronDownIcon size="12" strokeWidth="1.75" aria-hidden="true" />
    </summary>
    <ul class="kit-popover-card" aria-label="Forge fleet">
      {#each orderedHosts as host (host.nodeID)}
        {@const status = hostStatus(host)}
        {@const diagnostic = hostDiagnostic(host)}
        {@const navigable = host.kind !== "devbox" && Boolean(host.baseURL)}
        <li>
          {#if navigable}
            <a
              class="host-row"
              href={host.baseURL}
              aria-current={host.kind === "self" ? "page" : undefined}
              aria-busy={switchingNodeID === host.nodeID ? "true" : undefined}
              title={diagnostic || `Open ${hostName(host)}`}
              onclick={(event) => handleHostClick(event, host)}
            >
              {@render hostRowContent(host, status, diagnostic)}
            </a>
          {:else}
            <div class="host-row" title={diagnostic || undefined}>
              {@render hostRowContent(host, status, diagnostic)}
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  </details>
{:else if fallbackLabel}
  <span class="forge-selector-fallback">{fallbackLabel}</span>
{/if}

<style>
  .forge-selector {
    position: relative;
    min-width: 0;
    color: var(--text-primary);
  }

  summary {
    box-sizing: border-box;
    width: 100%;
    min-height: 30px;
    max-width: 180px;
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    padding: 3px 7px;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    background: var(--bg-surface);
    cursor: pointer;
    list-style: none;
    font-size: var(--font-size-sm);
  }

  summary::-webkit-details-marker {
    display: none;
  }

  summary:hover,
  summary:focus-visible {
    border-color: var(--border-strong);
    color: var(--text-primary);
  }

  .current-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .compact summary {
    max-width: 132px;
    min-height: 34px;
  }

  ul {
    position: absolute;
    z-index: 80;
    top: calc(100% + 4px);
    left: 0;
    width: max-content;
    min-width: 250px;
    max-width: min(320px, calc(100vw - 20px));
    margin: 0;
    padding: 4px;
    list-style: none;
  }

  .host-row {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: var(--space-3);
    padding: 7px 8px;
    border-radius: var(--radius-sm);
    color: inherit;
    text-decoration: none;
  }

  li a:hover,
  li a:focus-visible,
  li a[aria-current="page"] {
    background: var(--bg-hover);
  }

  li a[aria-busy="true"] {
    cursor: progress;
  }

  .host-copy,
  .host-heading {
    min-width: 0;
    display: flex;
    align-items: center;
  }

  .host-copy {
    flex-direction: column;
    align-items: flex-start;
    gap: 1px;
  }

  .host-heading {
    width: 100%;
    gap: var(--space-2);
  }

  .host-heading strong {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--font-size-sm);
  }

  .host-label {
    flex: 0 0 auto;
    padding: 1px 4px;
    border: 1px solid var(--border-muted);
    border-radius: 999px;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }

  .host-status,
  .host-diagnostic {
    max-width: 100%;
    overflow: hidden;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    line-height: 1.3;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .host-status {
    text-transform: capitalize;
  }

  .forge-selector-fallback {
    min-width: 0;
    overflow: hidden;
    color: var(--text-primary);
    font-weight: 700;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
