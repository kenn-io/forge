<script lang="ts">
  import { SelectDropdown } from "@kenn-io/kit-ui"
  import { Typeahead } from "@kenn-io/kit-ui"
  import type { SessionConfigOption } from "./chat-types.js"

  let {
    options,
    disabled,
    onchange,
  }: {
    options: SessionConfigOption[]
    disabled: boolean
    onchange: (id: string, value: string) => void
  } = $props()
</script>

<div class="session-options">
  {#each options as option (option.id)}
    {#if option.type === "select"}
      <div class="setting" title={option.description ?? undefined}>
        <span>{option.name}</span>
        {#if option.options.some((choice) => "group" in choice)}
          <Typeahead
            value={`value:${option.currentValue}`}
            options={option.options.map((choice, index) =>
              "group" in choice
                ? {
                    name: `group:${index}`,
                    label: choice.name,
                    expanded: true,
                    children: choice.options.map((item) => ({
                      name: `value:${item.value}`,
                      label: item.name,
                    })),
                  }
                : { name: `value:${choice.value}`, label: choice.name },
            )}
            fallbackLabel={option.currentValue}
            placeholder={option.name}
            triggerPrefix={`${option.name}:`}
            {disabled}
            onselect={(value) =>
              onchange(option.id, value.slice("value:".length))}
          />
        {:else}
          <SelectDropdown
            value={option.currentValue}
            options={option.options.flatMap((choice) =>
              "value" in choice
                ? [{ value: choice.value, label: choice.name }]
                : [],
            )}
            title={option.name}
            {disabled}
            onchange={(value) => onchange(option.id, value)}
          />
        {/if}
      </div>
    {/if}
  {/each}
</div>

<style>
  .session-options {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--space-3);
  }
  .setting {
    display: flex;
    flex-direction: column;
    min-width: 0;
    gap: var(--space-1);
    font-size: var(--font-size-xs);
    color: var(--text-secondary);
  }
</style>
