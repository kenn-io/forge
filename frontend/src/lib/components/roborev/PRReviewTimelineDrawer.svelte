<script lang="ts">
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import { BottomDock, Chip } from "@kenn-io/kit-ui";
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { PRRoborevTimeline } from "../../stores/roborev/pr-timeline.svelte.js";
  import SidebarStoreScope from "../workspace/SidebarStoreScope.svelte";
  import ReviewContent from "./ReviewContent.svelte";
  import ReviewDrawer from "./ReviewDrawer.svelte";
  import type { PREvent } from "../../api/types.js";
  import { findProviderEntry, roborevVerdictLabel, roborevVerdictTone } from "./pr-review-timeline.js";

  interface Props {
    timeline: PRRoborevTimeline;
    events: ReadonlyArray<PREvent>;
    headSha: string;
  }

  let { timeline, events, headSha }: Props = $props();
  const runtime = getAppRuntime();
  let drawerTab = $state<"review" | "log" | "prompt">("review");
  const providerEntry = $derived.by(() => {
    const key = timeline.getOpenProviderKey();
    return key === null ? null : findProviderEntry(events, headSha, key);
  });

  $effect(() => {
    const id = timeline.getSelectedJobId();
    const review = timeline.review;
    review.setSelectedJobId(id);
    drawerTab = "review";
    if (id === undefined) return;
    const execution = untrack(() => runtime.runCommand(review.loadReviewEffect(id), {
      operation: "load pull request Roborev review",
      safeContext: { jobId: id },
      onFailure: () => {},
    }));
    return execution.interrupt;
  });
</script>

<SidebarStoreScope stores={timeline.stores}>
  <ReviewDrawer activeTab={drawerTab} />
  <BottomDock
    open={providerEntry !== null}
    onclose={() => timeline.closeProvider()}
    ariaLabel="Review details"
    initialHeight="50vh"
    minHeight="200px"
    maxHeight="80vh"
    closeTitle="Close review details"
    closeAriaLabel="Close review details"
  >
    {#snippet header()}
      {#if providerEntry}
        <div class="provider-review-header">
          {#if !providerEntry.verdictStated}
            <Chip size="sm" tone={roborevVerdictTone(providerEntry)}>{roborevVerdictLabel(providerEntry)}</Chip>
          {/if}
          <span class="provider-review-author">{providerEntry.author}</span>
          {#if providerEntry.url}
            <a class="provider-review-link" href={providerEntry.url} target="_blank" rel="noopener noreferrer">
              Comment
              <ExternalLinkIcon size={12} strokeWidth={2} aria-hidden="true" />
            </a>
          {/if}
        </div>
      {/if}
    {/snippet}
    {#if providerEntry}
      <ReviewContent output={providerEntry.body} />
    {/if}
  </BottomDock>
</SidebarStoreScope>

<style>
  .provider-review-header {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
    font-size: var(--font-size-sm);
  }

  .provider-review-author {
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .provider-review-link {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-left: auto;
    color: var(--accent-blue);
    text-decoration: none;
  }

  .provider-review-link:hover {
    text-decoration: underline;
  }
</style>
