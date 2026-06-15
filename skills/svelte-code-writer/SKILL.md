---
name: svelte-code-writer
description: CLI tools for Svelte 5 documentation lookup and code analysis. MUST be used whenever creating, editing or analyzing any Svelte component (.svelte) or Svelte module (.svelte.ts/.svelte.js). If possible, this skill should be executed within the svelte-file-editor agent for optimal results.
---

# Svelte 5.55.0 Code Writer

## CLI Tools

This repo currently uses Svelte `5.55.0`.

You have access to pinned `@sveltejs/mcp@0.1.22` CLI for Svelte-specific assistance. Launch it through the repo's embedded Vite+ tool so package-manager policy and workspace environment stay consistent:

```bash
node ./node_modules/vite-plus/bin/vp exec -- bun x @sveltejs/mcp@0.1.22 <command>
```

Run these commands from the repository root. If your shell is already in `frontend/`, use `node ../node_modules/vite-plus/bin/vp exec -- bun x ...` instead. Do not use `npx` or `npm`.

### List Documentation Sections

```bash
node ./node_modules/vite-plus/bin/vp exec -- bun x @sveltejs/mcp@0.1.22 list-sections
```

Lists all available Svelte 5 and SvelteKit documentation sections with titles and paths.

### Get Documentation

```bash
node ./node_modules/vite-plus/bin/vp exec -- bun x @sveltejs/mcp@0.1.22 get-documentation "<section1>,<section2>,..."
```

Retrieves full documentation for specified sections. Use after `list-sections` to fetch relevant docs.

**Example:**

```bash
node ./node_modules/vite-plus/bin/vp exec -- bun x @sveltejs/mcp@0.1.22 get-documentation "$state,$derived,$effect"
```

### Svelte Autofixer

```bash
node ./node_modules/vite-plus/bin/vp exec -- bun x @sveltejs/mcp@0.1.22 svelte-autofixer "<code_or_path>" [options]
```

Analyzes Svelte code and suggests fixes for common issues.

**Options:**

- `--async` - Enable async Svelte mode (default: false)
- `--svelte-version` - Target version: 4 or 5 (default: 5). For this repo, use 5 because project Svelte version is 5.55.0.

**Examples:**

```bash
# Analyze inline code (escape $ as \$)
node ./node_modules/vite-plus/bin/vp exec -- bun x @sveltejs/mcp@0.1.22 svelte-autofixer '<script>let count = \$state(0);</script>' --svelte-version 5

# Analyze a file
node ./node_modules/vite-plus/bin/vp exec -- bun x @sveltejs/mcp@0.1.22 svelte-autofixer ./src/lib/Component.svelte --svelte-version 5

# Target Svelte 4
node ./node_modules/vite-plus/bin/vp exec -- bun x @sveltejs/mcp@0.1.22 svelte-autofixer ./Component.svelte --svelte-version 4
```

**Important:** When passing code with runes (`$state`, `$derived`, etc.) via the terminal, escape the `$` character as `\$` to prevent shell variable substitution.

## Workflow

1. **Uncertain about syntax?** Run pinned `list-sections` then `get-documentation` for relevant topics through Vite+
2. **Reviewing/debugging?** Run pinned `svelte-autofixer` on the code through Vite+ to detect issues
3. **Always validate** - Run `svelte-autofixer` with `--svelte-version 5` through Vite+ before finalizing any Svelte component in this repo
