<script lang="ts">
  import { Effect } from "effect";
  import { untrack } from "svelte";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import {
    renderMarkdownEffect,
    renderMarkdownSync,
    type RenderMarkdownOpts,
    type RepoContext,
  } from "../../utils/markdown.js";

  interface Props {
    raw: string;
    repo?: RepoContext | undefined;
    options?: RenderMarkdownOpts | undefined;
    transformHtml?: ((html: string) => string) | undefined;
  }

  const {
    raw,
    repo = undefined,
    options = undefined,
    transformHtml = undefined,
  }: Props = $props();
  const runtime = getAppRuntime();

  interface ResolvedMarkdown {
    raw: string;
    repoKey: string;
    interactiveTasks: boolean;
    collapseSingleLineBreaks: boolean;
    html: string;
  }

  let resolved = $state<ResolvedMarkdown | null>(null);
  const repoKey = $derived(
    repo === undefined
      ? ""
      : `${repo.provider}\0${repo.platformHost ?? ""}\0${repo.owner}\0${repo.name}\0${repo.repoPath}`,
  );
  const interactiveTasks = $derived(options?.interactiveTasks ?? false);
  const collapseSingleLineBreaks = $derived(options?.collapseSingleLineBreaks ?? false);
  const renderedHtml = $derived.by(() => {
    if (
      resolved !== null
      && resolved.raw === raw
      && resolved.repoKey === repoKey
      && resolved.interactiveTasks === interactiveTasks
      && resolved.collapseSingleLineBreaks === collapseSingleLineBreaks
    ) {
      return resolved.html;
    }
    return renderMarkdownSync(raw, repo, { collapseSingleLineBreaks });
  });
  // Parent refreshes can recreate the transform without changing the markdown.
  // Apply it separately so highlighted content never falls back to plain HTML.
  const html = $derived(transformHtml?.(renderedHtml) ?? renderedHtml);

  $effect(() => {
    const currentRaw = raw;
    const currentRepo = repo;
    const currentRepoKey = repoKey;
    const currentInteractiveTasks = interactiveTasks;
    const currentCollapse = collapseSingleLineBreaks;
    const execution = untrack(() => runtime.runCommand(
      renderMarkdownEffect(currentRaw, currentRepo, {
        interactiveTasks: currentInteractiveTasks,
        collapseSingleLineBreaks: currentCollapse,
      }).pipe(
        Effect.tap((rendered) => Effect.sync(() => {
          resolved = {
            raw: currentRaw,
            repoKey: currentRepoKey,
            interactiveTasks: currentInteractiveTasks,
            collapseSingleLineBreaks: currentCollapse,
            html: rendered,
          };
        })),
        Effect.asVoid,
      ),
      {
        operation: "render markdown",
        safeContext: { length: currentRaw.length },
        onFailure: () => {},
      },
    ));
    return execution.interrupt;
  });
</script>

{@html html}
