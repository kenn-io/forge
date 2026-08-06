<script lang="ts">
  import { Effect } from "effect";
  import type { Snippet } from "svelte";
  import { onMount } from "svelte";

  let {
    stores = $bindable<unknown>(),
    children,
  }: {
    stores?: unknown;
    children?: Snippet;
  } = $props();

  const noop = () => {};
  const mockStores = {
    settings: {
      hasConfiguredRepos: () => true,
      isSettingsLoaded: () => true,
      isModeVisible: () => true,
    },
    grouping: {
      getGroupByRepo: () => true,
    },
    events: {
      disconnect: noop,
      getConnectionState: () => "connected",
      getLastError: () => null,
    },
    sync: {
      pollingEffect: Effect.never,
    },
    pulls: {
      getSelectedPR: () => null,
      loadPulls: noop,
      selectPR: noop,
      clearSelection: noop,
    },
    issues: {
      getSelectedIssue: () => null,
      loadIssues: noop,
      selectIssue: noop,
      clearIssueSelection: noop,
    },
    activity: {
      loadActivity: noop,
    },
    detail: {
      getDetail: () => null,
    },
  };

  onMount(() => {
    stores = mockStores;
  });
</script>

{@render children?.()}
