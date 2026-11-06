<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";

  interface Props {
    loading: boolean;
    onclick: () => void;
  }

  const { loading, onclick }: Props = $props();
</script>

<div class="list-pagination">
  <Button size="sm" surface="soft" disabled={loading} {onclick}>{loading ? "Loading more…" : "Load more"}</Button>
</div>

<style>
  .list-pagination {
    display: flex;
    justify-content: center;
    padding: var(--space-4);
  }
</style>
