<script lang="ts">
  interface Props {
    commitSha: string;
    inputActive?: boolean;
  }

  const { commitSha, inputActive = true }: Props = $props();
</script>

<div data-testid="commit-diff-panel" data-input-active={String(inputActive)}>Commit {commitSha}</div>
