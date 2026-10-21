<script lang="ts">
  import { Button, Spinner } from "@kenn-io/kit-ui";
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { importWorklist, worklistFailureMessage } from "../api/worklists.js";
  import { getAppRuntime } from "../app/runtime-context.js";
  import { getStores } from "../context.js";
  import { buildWorklistRoute, buildWorklistsRoute } from "../routes.js";
  import { showFlash } from "../stores/flash.svelte.js";
  import { navigate, replaceUrl } from "../stores/router.svelte.js";

  interface Props {
    fragment: string;
  }

  let { fragment }: Props = $props();

  const runtime = getAppRuntime();
  const { worklists } = getStores();

  let failure = $state<string | null>(null);

  // Opening a link creates its list, or finds the one this list already made,
  // and replaces the link with it. The request is not cancelled when the page
  // is left; the list exists either way, but only the page that is still open
  // navigates.
  $effect(() => {
    const current = fragment;
    let open = true;
    failure = null;
    untrack(() =>
      runtime.runCommand(
        importWorklist(current).pipe(
          Effect.tap((created) =>
            Effect.sync(() => {
              if (!open) return;
              replaceUrl(buildWorklistRoute(created.worklist_id));
              const total = created.item_count + created.skipped_count;
              if (created.skipped_count > 0) {
                showFlash(`Left out ${created.skipped_count} of ${total} items: not tracked, not synced, or listed twice.`, {
                  tone: "warning",
                });
              }
            }),
          ),
          Effect.andThen(() => worklists.refreshSummariesEffect().pipe(Effect.ignore)),
        ),
        {
          operation: "import worklist",
          safeContext: { length: current.length },
          onFailure: (error) => {
            if (open) failure = worklistFailureMessage(error, "Could not confirm the worklist was created.");
          },
        },
      ),
    );
    return () => {
      open = false;
    };
  });
</script>

<div class="worklist-import" role={failure ? "alert" : "status"}>
  {#if failure}
    <span>{failure}</span>
    <Button onclick={() => navigate(buildWorklistsRoute())}>Open worklists</Button>
  {:else}
    <Spinner size={18} />Creating worklist
  {/if}
</div>

<style>
  .worklist-import {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-8) var(--space-5);
    color: var(--text-muted);
  }
</style>
