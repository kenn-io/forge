<script lang="ts">
  import Modal from "../shared/Modal.svelte";
  import type { LaunchTarget, QuickAction, RuntimeSession } from "../../api/types.js";
  import WorkspaceLauncher from "./WorkspaceLauncher.svelte";

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
    launchTargets: LaunchTarget[];
    sessions: RuntimeSession[];
    displayLabels?: Record<string, string>;
    launchingKey?: string | null;
    readonly?: boolean;
    quickActions?: QuickAction[];
    onClose: () => void;
    onLaunch: (targetKey: string) => void;
    onQuickAction?: (action: QuickAction) => void;
    onOpenSession: (sessionKey: string) => void;
  }

  const {
    open,
    launchTargets,
    sessions,
    displayLabels = {},
    launchingKey = null,
    readonly = false,
    quickActions = [],
    onClose,
    onLaunch,
    onQuickAction,
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
      <WorkspaceLauncher
        {launchTargets}
        {sessions}
        {displayLabels}
        {launchingKey}
        {readonly}
        {quickActions}
        onLaunch={onLaunch}
        {onQuickAction}
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
