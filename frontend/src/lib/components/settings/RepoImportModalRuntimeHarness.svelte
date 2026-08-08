<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import type { Settings } from "../../api/types.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import RepoImportModal from "./RepoImportModal.svelte";

  interface Props {
    open: boolean;
    onClose: () => void;
    onImported: (settings: Settings) => void;
  }

  let { open, onClose, onImported }: Props = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<RepoImportModal {open} {onClose} {onImported} />
