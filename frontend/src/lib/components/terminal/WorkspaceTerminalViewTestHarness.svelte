<script lang="ts">
  import type { ComponentProps } from "svelte";
  import SessionTerminalPool from "./SessionTerminalPool.svelte";
  import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";

  // The view renders portal slots, not terminals: the live terminals belong to
  // the app-level pool so a session can be promoted out of the workspace pane.
  // In the app that pool is mounted by WorkspaceHost, so a test that mounts the
  // view alone would have slots and no terminals.
  type ViewProps = ComponentProps<typeof WorkspaceTerminalView>;

  let props: ViewProps = $props();
</script>

<SessionTerminalPool />
<WorkspaceTerminalView {...props} />
