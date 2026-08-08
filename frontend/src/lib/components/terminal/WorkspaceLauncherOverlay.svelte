<script lang="ts">
  import type { ComponentProps } from "svelte";
  import Modal from "../shared/Modal.svelte";
  import type { LaunchTarget, RuntimeSession } from "../../api/types.js";
  import WorkspaceHome from "./WorkspaceHome.svelte";

  /**
   * The launch surface as a transient overlay rather than a tab.
   *
   * Inside a detail pane the workspace gets one pane, and spending it on a Home tab
   * that is only ever used to start something costs the terminal half its height.
   * The overlay is opened on demand (and automatically for a workspace with no
   * session yet), and closes as soon as a session exists to show.
   */
  interface Props {
    open: boolean;
    workspace: NonNullable<ComponentProps<typeof WorkspaceHome>["workspace"]>;
    launchTargets: LaunchTarget[];
    sessions: RuntimeSession[];
    displayLabels?: Record<string, string>;
    launchingKey?: string | null;
    readonly?: boolean;
    onClose: () => void;
    onLaunch: (targetKey: string) => void;
    onOpenSession: (sessionKey: string) => void;
  }

  const {
    open,
    workspace,
    launchTargets,
    sessions,
    displayLabels = {},
    launchingKey = null,
    readonly = false,
    onClose,
    onLaunch,
    onOpenSession,
  }: Props = $props();
</script>

{#if open}
  <Modal
    {open}
    title="Launch a session"
    showClose
    width={620}
    frameId="workspace-launcher"
    onClose={onClose}
  >
    <div class="launcher-body">
      <WorkspaceHome
        {workspace}
        {launchTargets}
        {sessions}
        {displayLabels}
        {launchingKey}
        {readonly}
        showHeader={false}
        onLaunch={onLaunch}
        onOpenSession={onOpenSession}
      />
    </div>
  </Modal>
{/if}

<style>
  .launcher-body {
    display: flex;
    flex-direction: column;
    min-width: 0;
    max-height: min(60vh, 520px);
    overflow-y: auto;
  }
</style>
