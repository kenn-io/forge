<script lang="ts">
  import CollapsibleDescription from "../../../packages/ui/src/components/detail/CollapsibleDescription.svelte";
</script>

<CollapsibleDescription
  source={"x".repeat(1_501)}
  copied={false}
  oncopy={() => {}}
>
  <div style="height: 640px">Long rendered description</div>
</CollapsibleDescription>
