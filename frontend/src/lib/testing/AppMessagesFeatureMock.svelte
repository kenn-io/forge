<script lang="ts">
  let {
    route,
  }: {
    route: {
      q: string | null;
      message: string | null;
    };
  } = $props();

  let count = $state(0);
</script>

<section data-testid="messages-feature">
  <p>Messages {route.q ?? "none"} {route.message ?? "none"}</p>
  <button type="button" onclick={() => (count += 1)}>
    Messages count {count}
  </button>
</section>
