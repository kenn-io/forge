<script lang="ts">
  import { Effect } from "effect";
  import { onDestroy, untrack } from "svelte";
  import type { DocsAPI } from "../../api/docs/api.js";
  import type { Folder } from "../../api/docs/types.js";
  import { makeAppRuntime } from "../../app/runtime.js";
  import { setAppRuntime } from "../../app/runtime-context.js";
  import { DocsWorkflow } from "../../stores/docs-workflow.js";
  import AddFolderDialog from "./AddFolderDialog.svelte";

  interface Props {
    open: boolean;
    api: DocsAPI;
    onClose: () => void;
    onAdded: (folder: Folder) => void;
    initialPath?: string;
    presentationSurfaceID?: string;
    presentationSessionID?: string;
    daemonRoster?: readonly string[];
    daemonRosterLoaded?: boolean;
  }

  let {
    open,
    api,
    onClose,
    onAdded,
    initialPath = "",
    presentationSurfaceID = "docs-workspace",
    presentationSessionID = "docs-test-session",
    daemonRoster = [],
    daemonRosterLoaded = true,
  }: Props = $props();
  const runtime = makeAppRuntime();
  setAppRuntime(untrack(() => runtime));
  $effect(() => {
    const surfaceID = presentationSurfaceID;
    const sessionID = presentationSessionID;
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* DocsWorkflow;
        yield* workflow.claimPresenter(surfaceID, sessionID, Effect.void);
      }),
      { operation: "claim test Docs presenter", safeContext: {}, onFailure: () => {} },
    );
    return () => {
      runtime.runCommand(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          yield* workflow.releasePresenter(surfaceID, sessionID);
        }),
        { operation: "release test Docs presenter", safeContext: {}, onFailure: () => {} },
      );
    };
  });
  onDestroy(() => {
    Effect.runFork(runtime.disposeEffect);
  });
</script>

<AddFolderDialog
  {open}
  {api}
  {onClose}
  {onAdded}
  {initialPath}
  {presentationSurfaceID}
  {presentationSessionID}
  {daemonRoster}
  {daemonRosterLoaded}
/>
