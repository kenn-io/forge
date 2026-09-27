<script lang="ts">
  import { Menu } from "@kenn-io/kit-ui"
  import { MenuTrigger } from "@kenn-io/kit-ui"
  import { MenuContent } from "@kenn-io/kit-ui"
  import { MenuRadioGroup } from "@kenn-io/kit-ui"
  import { MenuRadioItem } from "@kenn-io/kit-ui"
  import type { SessionConfigOption } from "./chat-types.js"
  import ChatEffortGlyph from "./ChatEffortGlyph.svelte"

  // One ACP select option as a composer toolbar chip with a radio menu. The
  // label shows the agent-confirmed value, not the pending choice.
  let {
    option,
    disabled,
    onchange,
  }: {
    option: SessionConfigOption
    disabled: boolean
    onchange: (value: string) => void
  } = $props()

  type Choice = { value: string; name: string }
  type Group = { label: string | null; choices: readonly Choice[] }

  const groups = $derived.by((): Group[] => {
    if (option.type !== "select") return []
    const loose: Choice[] = []
    const result: Group[] = []
    for (const choice of option.options) {
      if ("group" in choice)
        result.push({ label: choice.name, choices: choice.options })
      else loose.push(choice)
    }
    return loose.length ? [{ label: null, choices: loose }, ...result] : result
  })
  const flat = $derived(groups.flatMap((group) => group.choices))
  const level = $derived(
    flat.findIndex((choice) => choice.value === option.currentValue),
  )
  const current = $derived(flat[level]?.name ?? String(option.currentValue))
</script>

{#if option.type === "select"}
  <Menu
    align="start"
    class={option.category === "model"
      ? "opt opt--model"
      : option.category === "thought_level"
        ? "opt opt--fixed opt--effort"
        : "opt opt--fixed"}
  >
    <MenuTrigger
      class="tb-chip"
      ariaLabel={`${option.name}: ${current}`}
      title={`${option.name}: ${current}\n${option.description ?? "Applies when the agent next uses it."}`}
      {disabled}
    >
      {#if option.category === "thought_level" && level >= 0}<ChatEffortGlyph
          {level}
          levels={flat.length}
        />{/if}
      <span class="tb-chip__label">{current}</span>
    </MenuTrigger>
    <MenuContent ariaLabel={option.name}>
      <p class="menu-heading">{option.name}</p>
      <MenuRadioGroup
        value={String(option.currentValue)}
        {onchange}
        ariaLabel={option.name}
      >
        {#each groups as group, index (index)}
          {#if group.label}<p class="menu-heading">{group.label}</p>{/if}
          {#each group.choices as choice (choice.value)}
            <MenuRadioItem value={choice.value} closeOnSelect
              >{choice.name}</MenuRadioItem
            >
          {/each}
        {/each}
      </MenuRadioGroup>
    </MenuContent>
  </Menu>
{/if}

<style>
  .menu-heading {
    margin: 0;
    padding: var(--space-3) var(--space-4) var(--space-2);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: var(--font-weight-medium);
  }
</style>
