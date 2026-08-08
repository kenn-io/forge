<script lang="ts">
  import { untrack, type ComponentProps } from "svelte";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import RepoIssueModal from "./RepoIssueModal.svelte";

  const { runtime, modalProps }: {
    runtime: AppRuntime;
    modalProps: ComponentProps<typeof RepoIssueModal>;
  } = $props();

  setAppRuntime(untrack(() => runtime));
</script>

<RepoIssueModal {...modalProps} />
