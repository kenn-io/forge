<script lang="ts">
  import { ListEnd, type ListEndState } from "@kenn-io/kit-ui";

  let { rows, more, idle, label, onmore }: {
    rows: ListEndState;
    more: boolean;
    idle: boolean;
    label: string;
    onmore: () => void;
  } = $props();
</script>

<ul><ListEnd {rows} {more} {idle} {label} {onmore} /></ul>

<style>
  ul { margin: 0; padding: 0; list-style: none; container-type: inline-size; }
</style>
