<script lang="ts">
  let {
    route,
    onOpenKataShortId = undefined,
  }: {
    route: {
      folder: string | null;
      doc: string | null;
    };
    onOpenKataShortId?: ((shortId: string, project?: string, daemonId?: string) => void) | undefined;
  } = $props();

  let count = $state(0);
</script>

<section data-testid="docs-feature">
  <p>Docs {route.folder ?? "none"} {route.doc ?? "none"}</p>
  <button type="button" onclick={() => (count += 1)}>
    Docs count {count}
  </button>
  <button type="button" onclick={() => onOpenKataShortId?.("solo")}>Open Kata reference</button>
  <button type="button" onclick={() => onOpenKataShortId?.("closed-task")}>Open closed Kata reference</button>
</section>
