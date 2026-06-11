<script lang="ts">
  let {
    route,
  }: {
    route: {
      folder: string | null;
      doc: string | null;
    };
  } = $props();

  let count = $state(0);
</script>

<section data-testid="docs-feature">
  <p>Docs {route.folder ?? "none"} {route.doc ?? "none"}</p>
  <button type="button" onclick={() => (count += 1)}>
    Docs count {count}
  </button>
</section>
