<script lang="ts">
  import { untrack } from "svelte";
  import type { DocsAPI } from "../../api/docs/api.js";
  import type { GitPublishResponse } from "../../api/docs/types.js";
  import type { AppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import PublishDocsDialog from "./PublishDocsDialog.svelte";

  interface Props {
    runtime: AppRuntime;
    open: boolean;
    folderID: string;
    api: DocsAPI;
    onClose: () => void;
    onPublished: (result: GitPublishResponse) => void;
  }

  const { runtime, open, folderID, api, onClose, onPublished }: Props = $props();
  setAppRuntime(untrack(() => runtime));
</script>

<PublishDocsDialog {open} {folderID} {api} {onClose} {onPublished} />
