<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { setMobileNavMenuContext } from "../mobile/mobile-nav-menu.js";
  import PullDetail from "./PullDetail.svelte";

  const { runtime, detailProps, phoneShell = false }: {
    runtime: AppRuntime;
    detailProps: ComponentProps<typeof PullDetail>;
    phoneShell?: boolean;
  } = $props();

  setAppRuntime(untrack(() => runtime));
  if (untrack(() => phoneShell)) {
    setMobileNavMenuContext({
      page: () => "pulls",
      isModeVisible: () => true,
      onNavigate: () => {},
      onDesktopView: () => {},
    });
  }
</script>

<PullDetail {...detailProps} />
