<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import UserListEditor from "./UserListEditor.svelte";

  const { runtime, editorProps }: {
    runtime: AppRuntime;
    editorProps: ComponentProps<typeof UserListEditor>;
  } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<UserListEditor {...editorProps} />
