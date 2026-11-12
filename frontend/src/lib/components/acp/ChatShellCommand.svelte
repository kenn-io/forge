<!-- A shell command in code font. Shows plain text until Shiki highlights it. -->
<script lang="ts">
  import { Effect } from "effect"
  import { getAppRuntime } from "../../app/runtime-context.js"
  import { highlightShellCommandEffect, type ShellToken } from "../../utils/markdown.js"

  let { command }: { command: string } = $props()
  const runtime = getAppRuntime()
  let highlighted = $state.raw<{ source: string; tokens: readonly ShellToken[] } | null>(null)
  const tokens = $derived(highlighted?.source === command ? highlighted.tokens : null)

  $effect(() => {
    const source = command
    const execution = runtime.runCommand(
      highlightShellCommandEffect(source).pipe(
        Effect.tap((result) =>
          Effect.sync(() => {
            highlighted = { source, tokens: result }
          }),
        ),
        Effect.catch(() => Effect.void),
      ),
      {
        operation: "highlight chat shell command",
        safeContext: {},
        onFailure: () => {},
      },
    )
    return execution.interrupt
  })
</script>

<code class="command"
  >{#if tokens}{#each tokens as token, index (index)}<span
        style:--shiki-light={token.light}
        style:--shiki-dark={token.dark}>{token.content}</span
      >{/each}{:else}{command}{/if}</code
>

<style>
  .command {
    font-family: var(--font-mono);
  }
  .command span {
    color: var(--shiki-light, inherit);
  }
  :global(:root.dark) .command span {
    color: var(--shiki-dark, inherit);
  }
</style>
