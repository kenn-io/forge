<script lang="ts">
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import { BottomDock, Chip } from "@kenn-io/kit-ui";
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { PRRoborevTimeline } from "../../stores/roborev/pr-timeline.svelte.js";
  import SidebarStoreScope from "../workspace/SidebarStoreScope.svelte";
  import ReviewContent from "./ReviewContent.svelte";
  import ReviewDrawer from "./ReviewDrawer.svelte";
  import { roborevVerdictLabel, roborevVerdictTone } from "./pr-review-timeline.js";

  interface Props {
    timeline: PRRoborevTimeline;
  }

  let { timeline }: Props = $props();
  const runtime = getAppRuntime();
  let drawerTab = $state<"review" | "log" | "prompt">("review");
  const providerEntry = $derived(timeline.getOpenProviderEntry());

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
          <Chip size="sm" tone={roborevVerdictTone(providerEntry)}>{roborevVerdictLabel(providerEntry)}</Chip>
          {#if providerEntry.commit}
            <code class="provider-review-commit">{providerEntry.commit.slice(0, 7)}</code>
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

  .provider-review-commit {
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
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
