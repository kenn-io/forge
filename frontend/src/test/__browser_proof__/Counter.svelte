<script lang="ts">
  let count = $state(0);
</script>

<button type="button" data-testid="counter" onclick={() => (count += 1)}>
  count is {count}
</button>
