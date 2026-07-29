<script lang="ts">
  import CollapsibleDescription from "../../../packages/ui/src/components/detail/CollapsibleDescription.svelte";
</script>

<CollapsibleDescription
  source={"x".repeat(1_501)}
  itemKey="github:github.com:acme/widget:pull:1"
  copied={false}
  oncopy={() => {}}
>
  <div style="height: 640px">Long rendered description</div>
</CollapsibleDescription>
