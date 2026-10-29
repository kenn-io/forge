<script module lang="ts">
  // The presenter that reported an error owns the claim until it unmounts, so
  // sibling presenters of one attempt count once and a remounted retry counts again.
  const errors = new Map<string, object>();
</script>

<script lang="ts">
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { reportOutcome, type Outcome } from "../../app/telemetry.js";
  import { documentVisible } from "../../browser/document-visible.svelte.js";

  const { kind, scope, shown = true }: { kind: Extract<Outcome, { event: "error_shown" }>["kind"]; scope: string; shown?: boolean } = $props();
  const runtime = getAppRuntime();
  const self = {};
  let counted: string | undefined;
  $effect(() => {
    const key = `${kind}:${scope}`;
    return () => {
      if (errors.get(key) === self) errors.delete(key);
    };
  });
  function claim(key: string): void {
    counted = key;
    if (errors.has(key)) return;
    errors.set(key, self);
    untrack(() => reportOutcome(runtime, { event: "error_shown", kind }));
  }
  $effect(() => {
    const key = `${kind}:${scope}`;
    if (counted !== key && shown && documentVisible()) claim(key);
  });
</script>
