<script lang="ts">
  interface Props {
    commitSha: string;
  }

  const { commitSha }: Props = $props();
</script>

<div data-testid="commit-diff-panel">Commit {commitSha}</div>
