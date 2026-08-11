<script lang="ts">
  let {
    route,
    onOpenKataReference = undefined,
  }: {
    route: {
      folder: string | null;
      doc: string | null;
    };
    onOpenKataReference?: ((
      reference: string,
      project?: string,
      daemonId?: string,
      kind?: "reference" | "uid",
    ) => void) | undefined;
  } = $props();

  let count = $state(0);
</script>

<section data-testid="docs-feature">
  <p>Docs {route.folder ?? "none"} {route.doc ?? "none"}</p>
  <button type="button" onclick={() => (count += 1)}>
    Docs count {count}
  </button>
  <button type="button" onclick={() => onOpenKataReference?.("solo", undefined, "docs-daemon")}>Open Kata reference</button>
  <button type="button" onclick={() => onOpenKataReference?.("completed-bare", undefined, "docs-daemon")}>Open completed bare Kata reference</button>
  <button type="button" onclick={() => onOpenKataReference?.("closed-task", "Project A", "docs-daemon")}>Open closed Kata reference</button>
  <button type="button" onclick={() => onOpenKataReference?.("issue-stable", undefined, "docs-daemon", "uid")}>Open Kata UID</button>
</section>
