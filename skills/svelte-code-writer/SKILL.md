---
name: svelte-code-writer
description: CLI tools for Svelte 5 documentation lookup and code analysis. MUST be used whenever creating, editing or analyzing any Svelte component (.svelte) or Svelte module (.svelte.ts/.svelte.js). If possible, this skill should be executed within the svelte-file-editor agent for optimal results.
---

# Svelte 5.55.0 Code Writer

## CLI Tools

This repo currently uses Svelte `5.55.0`.

Use the repo-installed `@sveltejs/mcp@0.1.22` CLI for Svelte-specific assistance. It is pinned in root `devDependencies` and exposed through the repo's Vite+ launcher:

```bash
bun run svelte-mcp -- <command>
```

Run these commands from the repository root. Do not use `npx`, `npm`, or `bun x` for this tool.

### List Documentation Sections

```bash
bun run svelte-mcp -- list-sections
```

Lists all available Svelte 5 and SvelteKit documentation sections with titles and paths.

### Get Documentation

```bash
bun run svelte-mcp -- get-documentation "<section1>,<section2>,..."
```

Retrieves full documentation for specified sections. Use after `list-sections` to fetch relevant docs.

**Example:**

```bash
bun run svelte-mcp -- get-documentation "$state,$derived,$effect"
```

### Svelte Autofixer

```bash
bun run svelte-mcp -- svelte-autofixer "<code_or_path>" [options]
```

Analyzes Svelte code and suggests fixes for common issues.

**Options:**

- `--async` - Enable async Svelte mode (default: false)
- `--svelte-version` - Target version: 4 or 5 (default: 5). For this repo, use 5 because project Svelte version is 5.55.0.

**Examples:**

```bash
# Analyze inline code
bun run svelte-mcp -- svelte-autofixer '<script>let count = $state(0);</script>' --svelte-version 5

# Analyze a file
bun run svelte-mcp -- svelte-autofixer ./src/lib/Component.svelte --svelte-version 5

# Target Svelte 4
bun run svelte-mcp -- svelte-autofixer ./Component.svelte --svelte-version 4
```

**Important:** When passing code with runes (`$state`, `$derived`, etc.) via the terminal, wrap inline code in single quotes. If you use double quotes, escape `$` as `\$` to prevent shell variable substitution.

## Workflow

1. **Uncertain about syntax?** Run pinned `list-sections` then `get-documentation` for relevant topics with `bun run svelte-mcp --`
2. **Reviewing/debugging?** Run pinned `svelte-autofixer` on the code with `bun run svelte-mcp --` to detect issues
3. **Always validate** - Run `svelte-autofixer` with `--svelte-version 5` before finalizing any Svelte component in this repo
