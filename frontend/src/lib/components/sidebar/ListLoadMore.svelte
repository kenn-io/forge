<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";

  interface Props {
    loading: boolean;
    onclick: () => void;
    label?: string;
    size?: "sm" | "md";
  }

  const { loading, onclick, label = "Load more", size = "sm" }: Props = $props();
</script>

<div class="list-pagination">
  <Button {size} surface="soft" disabled={loading} {onclick}>{loading ? "Loading more…" : label}</Button>
</div>

<style>
  .list-pagination {
    display: flex;
    justify-content: center;
    padding: var(--space-4);
  }
</style>
