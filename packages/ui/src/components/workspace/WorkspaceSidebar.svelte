<script lang="ts">
  import type { WorkspaceData } from "../../api/types.js";
  import ProjectSection from "./ProjectSection.svelte";
  import SessionSection from "./SessionSection.svelte";
  import ResourceFooter from "./ResourceFooter.svelte";

  interface Props {
    workspaceData: WorkspaceData;
    hoverCardsEnabled?: boolean;
    onCommand: (
      command: string,
      payload: Record<string, unknown>,
    ) => void;
  }

  let {
    workspaceData,
    hoverCardsEnabled = false,
    onCommand,
  }: Props = $props();

  const selectedHost = $derived(
    workspaceData.hosts.find(
      (h) => h.key === workspaceData.selectedHostKey,
    ) ?? workspaceData.hosts[0],
  );

  const repositoryProjects = $derived(
    selectedHost?.projects.filter(
      (p) => p.kind === "repository",
    ) ?? [],
  );

  const scratchProjects = $derived(
    selectedHost?.projects.filter(
      (p) => p.kind === "scratch",
    ) ?? [],
  );

  const multipleHosts = $derived(
    workspaceData.hosts.length > 1,
  );

  function selectHost(key: string): void {
    onCommand("selectHost", { hostKey: key });
  }

  function retryHost(key: string): void {
    onCommand("retryHost", { hostKey: key });
  }


  function addRepository(): void {
    onCommand("addRepository", {
      hostKey: selectedHost?.key ?? "",
    });
  }

  function singleHostStatusText(state: string): string {
    switch (state) {
      case "connecting":
        return "Connecting…";
      case "disconnected":
        return "Disconnected";
      case "error":
        return "Connection error";
      default:
        return "";
    }
  }

  function singleHostStatusLabel(
    host: { label: string; connectionState: string },
  ): string {
    const status = singleHostStatusText(
      host.connectionState,
    );
    return status === ""
      ? `Host ${host.label} connected`
      : `Host ${host.label} ${status}`;
  }
</script>

<div class="workspace-sidebar">
  {#if multipleHosts}
    <div class="host-switcher">
      {#each workspaceData.hosts as host (host.key)}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="host-btn"
          class:active={host.key === selectedHost?.key}
          onclick={() => selectHost(host.key)}
          onkeydown={(e: KeyboardEvent) => {
            if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) {
              e.preventDefault();
              selectHost(host.key);
            }
          }}
          role="button"
          tabindex="0"
        >
          <span
            class="connection-dot"
            class:connected={
              host.connectionState === "connected"
            }
            class:connecting={
              host.connectionState === "connecting"
            }
            class:disconnected={
              host.connectionState === "disconnected"
            }
            class:error={
              host.connectionState === "error"
            }
          ></span>
          <span class="host-label">{host.label}</span>
          {#if host.transport}
            <span class="transport-badge">{host.transport.toUpperCase()}</span>
          {/if}
          {#if host.platform}
            <span class="platform-icon" title={host.platform}>
              {host.platform === "macOS" ? "\uD83D\uDCBB" : "\uD83D\uDDA5"}
            </span>
          {/if}
          <span class="conn-dot status-{host.connectionState}"></span>
          {#if host.connectionState === "disconnected" || host.connectionState === "error"}
            <button
              class="retry-btn"
              onclick={(e: MouseEvent) => {
                e.stopPropagation();
                retryHost(host.key);
              }}
            >
              Retry
            </button>
          {/if}
        </div>
      {/each}
    </div>
  {:else if selectedHost}
    <div class="host-switcher">
      <div
        class="host-chip"
        data-testid="single-host-chip"
        role="status"
        aria-label={singleHostStatusLabel(selectedHost)}
      >
        <span
          class="connection-dot"
          class:connected={
            selectedHost.connectionState === "connected"
          }
          class:connecting={
            selectedHost.connectionState === "connecting"
          }
          class:disconnected={
            selectedHost.connectionState === "disconnected"
          }
          class:error={
            selectedHost.connectionState === "error"
          }
        ></span>
        <span class="host-label">{selectedHost.label}</span>
        {#if selectedHost.transport}
          <span class="transport-badge">{selectedHost.transport.toUpperCase()}</span>
        {/if}
        {#if selectedHost.platform}
          <span class="platform-icon" title={selectedHost.platform}>
            {selectedHost.platform === "macOS" ? "\uD83D\uDCBB" : "\uD83D\uDDA5"}
          </span>
        {/if}
        {#if selectedHost.connectionState !== "connected"}
          <span class="status-text">
            {singleHostStatusText(selectedHost.connectionState)}
          </span>
        {/if}
        {#if selectedHost.connectionState === "disconnected" || selectedHost.connectionState === "error"}
          <button
            class="retry-btn"
            onclick={() => retryHost(selectedHost.key)}
          >
            Retry
          </button>
        {/if}
      </div>
    </div>
  {/if}

  <div class="scroll-area">
    {#if selectedHost}
      {#each scratchProjects as project (project.key)}
        <ProjectSection
          {project}
          hostKey={selectedHost.key}
          selectedWorktreeKey={
            workspaceData.selectedWorktreeKey
          }
          {hoverCardsEnabled}
          {onCommand}
        />
      {/each}

      {#each repositoryProjects as project (project.key)}
        <ProjectSection
          {project}
          hostKey={selectedHost.key}
          selectedWorktreeKey={
            workspaceData.selectedWorktreeKey
          }
          {hoverCardsEnabled}
          {onCommand}
        />
      {/each}

      <SessionSection
        sessions={selectedHost.sessions}
        hostKey={selectedHost.key}
        {onCommand}
      />
    {/if}
  </div>

  <div class="footer">
    {#if selectedHost}
      <button class="add-repo-btn" onclick={addRepository}>
        Add Repository
      </button>
    {/if}
    {#if selectedHost}
      <ResourceFooter
        resources={selectedHost.resources}
      />
    {/if}
  </div>
</div>

<style>
  .workspace-sidebar {
    display: flex;
    flex-direction: column;
    /* Fill the parent pane horizontally. Parents like
       `.sidebar-pane` in WorkspacesView.svelte are
       `display: flex` row containers, so without an explicit
       width the sidebar shrinks to its widest child and leaves
       visible whitespace on the right of the column. */
    width: 100%;
    min-width: 0;
    height: 100%;
    background: var(--bg-primary);
  }

  .host-switcher {
    display: flex;
    gap: 4px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-muted);
    flex-shrink: 0;
  }

  .host-btn {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    height: 28px;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    background: var(--bg-surface);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm, 4px);
    cursor: pointer;
    transition: background 0.1s;
    white-space: nowrap;
  }

  .host-btn:hover {
    background: var(--bg-surface-hover);
  }

  .host-btn.active {
    background: var(--accent-blue);
    color: var(--text-on-accent);
    border-color: var(--accent-blue);
  }

  .host-label {
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .host-chip {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    height: 28px;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    background: var(--bg-surface);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm, 4px);
    white-space: nowrap;
    /* Allow the label inside to ellipsize instead of forcing
       the chip to overflow the sidebar on long host names. */
    min-width: 0;
    max-width: 100%;
    flex: 1 1 auto;
  }

  .status-text {
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .connection-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
    background: var(--text-muted);
  }

  .connection-dot.connected {
    background: var(--accent-green);
  }

  .connection-dot.connecting {
    background: var(--accent-amber);
  }

  .connection-dot.disconnected {
    background: var(--text-muted);
  }

  .connection-dot.error {
    background: var(--accent-red);
  }

  .transport-badge {
    font-size: 0.9em;
    font-weight: 600;
    padding: 1px 4px;
    border-radius: 3px;
    background: color-mix(
      in srgb,
      var(--text-muted) 15%,
      transparent
    );
    color: var(--text-secondary);
  }

  .platform-icon {
    font-size: var(--font-size-sm);
  }

  .conn-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .status-connected {
    background: var(--accent-green);
  }

  .status-connecting {
    background: var(--accent-blue);
  }

  .status-error {
    background: var(--accent-amber);
  }

  .status-disconnected {
    background: var(--accent-red);
  }

  .retry-btn {
    flex-shrink: 0;
    padding: 2px 8px;
    font-size: var(--font-size-xs);
    color: var(--accent-amber);
    background: none;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm, 4px);
    cursor: pointer;
  }

  .retry-btn:hover {
    background: var(--bg-surface-hover);
  }

  .scroll-area {
    flex: 1;
    overflow-y: auto;
    min-height: 0;
  }

  .footer {
    flex-shrink: 0;
    border-top: 1px solid var(--border-muted);
  }

  .add-repo-btn {
    display: block;
    width: 100%;
    padding: 8px 10px;
    font-size: var(--font-size-md);
    color: var(--text-secondary);
    text-align: center;
    background: var(--bg-surface);
    border: none;
    cursor: pointer;
    transition: background 0.1s;
  }

  .add-repo-btn:hover {
    background: var(--bg-surface-hover);
  }
</style>
