<script lang="ts">
  interface Props {
    path: string;
    contents: string;
  }

  const { path, contents }: Props = $props();
</script>

<pre data-testid="repo-browser-pierre-file-contents" data-path={path}><code>{contents}</code></pre>
