<script lang="ts">
  import { dismissable, StatusDot } from "@kenn-io/kit-ui";
  import type { RelayActivity, RelayStatus } from "../../api/generated/models/index.js";
  import { localDateTimeLabel } from "../../utils/time.js";

  let { status }: { status: RelayStatus } = $props();
  let open = $state(false);
  let wrapper: HTMLDivElement | undefined = $state();

  $effect(() => {
    if (!open) return;
    return dismissable({
      owners: () => [wrapper],
      dismiss: () => { open = false; },
      escapeFocus: () => wrapper?.querySelector("button"),
    });
  });

  function activityLabel(event: RelayActivity): string {
    switch (event.target) {
      case "pull_request": return `PR #${event.number}`;
      case "pull_request_checks": return `PR #${event.number} checks`;
      case "issue": return `Issue #${event.number}`;
      case "repository_refs": return "Branches and tags";
      case "repository": return "Repository";
    }
  }
</script>

<div class="relay-wrapper" bind:this={wrapper}>
  <button
    type="button"
    class="relay-button"
    aria-label="Show relay activity"
    aria-haspopup="dialog"
    aria-expanded={open}
    onclick={() => { open = !open; }}
  >
    <StatusDot status={status.unavailable ? "stale" : "working"} label={status.unavailable ? "Relay unavailable" : "Relay connected"} size={5} />
    Relay
  </button>
  {#if open}
    <div class="relay-popover kit-popover-card" role="dialog" aria-label="Recent relay activity">
      <h2>Recent relay activity</h2>
      {#if status.unavailable}
        <p class="relay-error">Could not reach the relay. Normal syncing continues.</p>
      {:else if status.last_poll_at}
        <p>Last checked {localDateTimeLabel(status.last_poll_at)}</p>
      {/if}
      {#if status.recent.length === 0}
        <p>No recent activity for your repositories.</p>
      {:else}
        <ul>
          {#each status.recent as event (event.cursor)}
            <li>
              <span class="repository">{event.repository}</span>
              <span>{activityLabel(event)}</span>
              <time datetime={event.received_at}>{localDateTimeLabel(event.received_at)}</time>
            </li>
          {/each}
        </ul>
      {/if}
      <p>Changes received by this Forge since startup. Refreshes may wait for API quota.</p>
    </div>
  {/if}
</div>

<style>
  .relay-wrapper {
    position: relative;
  }
  .relay-button {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: 1px 4px;
    border: 0;
    border-radius: var(--radius-sm);
    background: transparent;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
  .relay-button:hover {
    background: var(--bg-hover);
  }
  .relay-popover {
    position: absolute;
    right: 0;
    bottom: calc(100% + 4px);
    width: 300px;
    max-width: calc(100vw - var(--space-4));
    max-height: 360px;
    overflow-y: auto;
    padding: var(--space-5);
    z-index: var(--z-popover);
    font-size: var(--font-size-xs);
    white-space: normal;
    overflow-wrap: anywhere;
  }
  h2 {
    margin: 0;
    font-size: var(--font-size-xs);
    font-weight: 600;
  }
  p, time {
    color: var(--text-muted);
  }
  p {
    margin: var(--space-3) 0 0;
  }
  .relay-error {
    color: var(--accent-red);
  }
  ul {
    list-style: none;
    padding: 0;
    margin: var(--space-4) 0;
  }
  li {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    padding: var(--space-3) 0;
    border-top: 1px solid var(--border-muted);
  }
  .repository {
    color: var(--text-primary);
    font-weight: 600;
  }
</style>
