<script lang="ts">
  import { File as PierreFile } from "@pierre/diffs";
  import type { FileContents, FileOptions, ThemeTypes } from "@pierre/diffs";
  import { onMount } from "svelte";

  interface Props {
    path: string;
    contents: string;
    tabWidth?: number;
    wordWrap?: boolean;
  }

  const { path, contents, tabWidth = 2, wordWrap = false }: Props = $props();

  let host: HTMLElement | undefined = $state();
  let pierreFile: PierreFile<undefined> | undefined;
  let themeType = $state<ThemeTypes>(appThemeType());
  let renderFailed = $state(false);

  const fileContents = $derived<FileContents>({
    name: path,
    contents,
    cacheKey: fileContentsCacheKey(path, contents),
  });

  const options = $derived.by<FileOptions<undefined>>(() => ({
    disableFileHeader: true,
    disableVirtualizationBuffers: true,
    overflow: wordWrap ? "wrap" : "scroll",
    theme: { dark: "pierre-dark", light: "pierre-light" },
    themeType,
    tokenizeMaxLineLength: 180,
    unsafeCSS: `
      :host {
        display: block;
        min-height: 100%;
        font-family: var(--font-mono);
        --diffs-font-family: var(--font-mono);
        --diffs-tab-size: ${tabWidth};
        --diffs-light-bg: var(--bg-primary, #fff);
        --diffs-dark-bg: var(--bg-primary, #0d0d12);
      }
      pre {
        min-height: 100%;
        margin: 0;
        border-radius: 0;
        background: var(--bg-primary, transparent);
        font: inherit;
        line-height: 1.55;
      }
    `,
  }));

  onMount(() => {
    let themeObserver: MutationObserver | undefined;
    if (typeof MutationObserver !== "undefined") {
      themeObserver = new MutationObserver(() => {
        themeType = appThemeType();
      });
      themeObserver.observe(document.documentElement, {
        attributeFilter: ["class"],
      });
    }

    return () => {
      themeObserver?.disconnect();
      pierreFile?.cleanUp();
      pierreFile = undefined;
    };
  });

  $effect(() => {
    if (!host) return;
    try {
      renderFailed = false;
      pierreFile ??= new PierreFile<undefined>(options, undefined, true);
      pierreFile.setOptions(options);
      pierreFile.render({
        file: fileContents,
        fileContainer: host,
        forceRender: true,
        renderRange: {
          startingLine: 0,
          totalLines: Number.POSITIVE_INFINITY,
          bufferBefore: 0,
          bufferAfter: 0,
        },
      });
    } catch {
      renderFailed = true;
      pierreFile?.cleanUp();
      pierreFile = undefined;
    }
  });

  $effect(() => {
    pierreFile?.setThemeType(themeType);
  });

  function appThemeType(): ThemeTypes {
    if (typeof document === "undefined") return "system";
    return document.documentElement.classList.contains("dark") ? "dark" : "light";
  }

  function fileContentsCacheKey(filePath: string, text: string): string {
    let hash = 2166136261;
    for (let index = 0; index < text.length; index += 1) {
      hash ^= text.charCodeAt(index);
      hash = Math.imul(hash, 16777619);
    }
    return `${filePath}\0${text.length}\0${hash >>> 0}`;
  }
</script>

<diffs-container
  bind:this={host}
  class="pierre-file-contents"
  class:pierre-file-contents--hidden={renderFailed}
  data-testid="repo-browser-pierre-file-contents"
></diffs-container>
{#if renderFailed}
  <pre class="pierre-file-contents__fallback" data-testid="repo-browser-plaintext-file-contents"><code>{contents}</code></pre>
{/if}

<style>
  .pierre-file-contents {
    width: 100%;
    min-width: max-content;
    min-height: 100%;
  }

  .pierre-file-contents--hidden {
    display: none;
  }

  .pierre-file-contents__fallback {
    min-width: max-content;
    min-height: 100%;
    margin: 0;
    background: var(--bg-primary, transparent);
    color: var(--text-primary);
    font: inherit;
    line-height: 1.55;
    tab-size: var(--diffs-tab-size, 2);
    white-space: pre;
  }
</style>
