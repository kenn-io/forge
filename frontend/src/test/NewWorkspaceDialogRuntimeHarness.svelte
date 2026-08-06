<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";

  import { makeAppRuntime } from "../lib/app/runtime.js";
  import { setAppRuntime } from "../lib/app/runtime-context.js";
  import NewWorkspaceDialog from "../lib/components/terminal/NewWorkspaceDialog.svelte";

  interface Props {
    open: boolean;
    onClose: () => void;
    onCreated: (workspaceId: string) => void;
  }

  let { open, onClose, onCreated }: Props = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<NewWorkspaceDialog {open} {onClose} {onCreated} />
