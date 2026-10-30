<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { setMobileNavMenuContext } from "../mobile/mobile-nav-menu.js";
  import UserListEditor from "./UserListEditor.svelte";

  const { runtime, editorProps, phoneShell = false }: {
    runtime: AppRuntime;
    editorProps: ComponentProps<typeof UserListEditor>;
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

<UserListEditor {...editorProps} />
