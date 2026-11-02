<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack, type ComponentProps } from "svelte";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { setMobileNavMenuContext } from "../mobile/mobile-nav-menu.js";
  import ApproveButton from "./ApproveButton.svelte";

  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
  const { phoneShell = false, ...props }: ComponentProps<typeof ApproveButton> & { phoneShell?: boolean } = $props();
  if (untrack(() => phoneShell)) {
    setMobileNavMenuContext({
      page: () => "pulls",
      isModeVisible: () => true,
      onNavigate: () => {},
      onDesktopView: () => {},
    });
  }
</script>

<ApproveButton {...props} />
