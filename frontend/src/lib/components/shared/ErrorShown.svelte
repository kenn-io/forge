<script module lang="ts">
  const errors = new Map<string, { mounted: number; reported: boolean }>();
</script>

<script lang="ts">
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { reportOutcome, type Outcome } from "../../app/telemetry.js";
  import { documentVisible } from "../../browser/document-visible.svelte.js";

  const { kind, scope, shown = true }: { kind: Extract<Outcome, { event: "error_shown" }>["kind"]; scope: string; shown?: boolean } = $props();
  const runtime = getAppRuntime();
  let entry = $state.raw<{ mounted: number; reported: boolean }>();
  $effect(() => {
    const key = `${kind}:${scope}`;
    const current = errors.get(key) ?? { mounted: 0, reported: false };
    current.mounted++;
    errors.set(key, current);
    entry = current;
    return () => {
      if (--current.mounted === 0) errors.delete(key);
    };
  });
  $effect(() => {
    if (entry && shown && documentVisible() && !entry.reported) {
      entry.reported = true;
      untrack(() => reportOutcome(runtime, { event: "error_shown", kind }));
    }
  });
</script>
