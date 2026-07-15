<script lang="ts">
  interface Props {
    active?: boolean;
  }

  let { active = false }: Props = $props();
</script>

<div data-testid="terminal-pane" data-active={active}></div>
