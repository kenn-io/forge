<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { getInlineWorkspaceController } from "../../stores/workspace-host.svelte.ts";
  import SessionTerminalPool from "./SessionTerminalPool.svelte";
  import WorkspaceTerminalView from "./WorkspaceTerminalView.svelte";

  // The view renders portal slots, not terminals: the live terminals belong to
  // the app-level pool so a session can be promoted out of the workspace pane.
  // In the app that pool is mounted by WorkspaceHost, so a test that mounts the
  // view alone would have slots and no terminals.
  type ViewProps = ComponentProps<typeof WorkspaceTerminalView>;
  type Props = ViewProps & { runtime?: AppRuntime };

  let { runtime, ...props }: Props = $props();
  const initialRuntime = untrack(() => runtime);
  if (initialRuntime) {
    setAppRuntime(initialRuntime);
  }
  const externalDock = $derived(
    props.paneSurface
      ? getInlineWorkspaceController(props.paneSurface).dockRow()
      : null,
  );
</script>

<SessionTerminalPool />
<WorkspaceTerminalView {...props} />
{@render externalDock?.()}
