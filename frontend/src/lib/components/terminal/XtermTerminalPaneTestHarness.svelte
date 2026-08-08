<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import XtermTerminalPane from "./XtermTerminalPane.svelte";

  type Props = ComponentProps<typeof XtermTerminalPane> & { runtime: AppRuntime };

  const { runtime, ...props }: Props = $props();
  setAppRuntime(untrack(() => runtime));
</script>

<XtermTerminalPane {...props} />
