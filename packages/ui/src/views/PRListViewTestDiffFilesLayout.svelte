<script lang="ts">
  interface Props {
    owner: string;
    name: string;
    number: number;
  }

  const { owner, name, number }: Props = $props();
</script>

<div data-testid="diff-files">Files changed {owner}/{name}#{number}</div>
