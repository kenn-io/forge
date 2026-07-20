# Workspace Agent Override Design

## Goal

Deliver Middleman's generated workspace context to Codex-family agents through
Codex's actual override mechanism without suppressing repository-owned
instructions. Continue delivering context to Claude-family agents through
Claude's local instruction mechanism.

## Agent Family Detection

Instruction-file selection happens from the launch target name after trimming
surrounding whitespace. A case-folded prefix match selects the agent family:

- names beginning with `codex` use the Codex behavior;
- names beginning with `claude` use the Claude behavior;
- all other names receive no generated instruction file.

The comparison is performed at the launch-context boundary rather than relying
on configuration normalization. Names such as `codex proxy`, `Codex yolo`, and
case variants of `claude` therefore select the intended behavior.

## Codex Override Composition

Codex-family launches generate `AGENTS.override.md` in the workspace root. Its
content is composed in this order:

1. the existing rendered Middleman workspace context, including the ownership
   marker and untrusted-source-text boundary;
2. a separating newline when repository instructions are appended;
3. the root `AGENTS.md` content, preserved verbatim.

Codex treats `AGENTS.override.md` as a replacement for `AGENTS.md`, so copying
the regular instructions after the generated context preserves both instruction
sets inside the one effective file. Middleman never modifies root `AGENTS.md`.

If root `AGENTS.md` is absent or cannot be read, launch continues and Middleman
writes a context-only `AGENTS.override.md`. This fallback keeps arbitrary
workspaces launchable while still delivering source identity.

## Claude Local Context

Claude-family launches continue generating context-only `CLAUDE.local.md`.
This file remains additive under Claude's instruction loading behavior, so root
`CLAUDE.md` is not copied into it.

## Ownership and Git Safety

The existing first-line marker remains the ownership signal. Middleman may
create or refresh a marked generated file, but it preserves an unmarked regular
file, symlink, directory, or other non-regular entry. Writes remain atomic.

`AGENTS.override.md` and `CLAUDE.local.md` are allowlisted for the workspace's
private Git exclude file. Root `AGENTS.md` and `CLAUDE.md` remain protected from
generated ignore rules. Middleman stops generating `AGENTS.local.md`; existing
copies are not deleted as part of this change.

## Error Handling

Failure to inspect, ignore, or atomically write the generated target remains a
launch-preparation error, except that failure to read root `AGENTS.md` is the
explicit context-only fallback. A user-owned target remains the existing silent
degraded state: launch continues without rewriting that file.

## Testing

Focused workspace tests will cover:

- case-folded prefix selection for Codex and Claude names, including names with
  suffix text;
- no generated path for unrelated agent names;
- generated context appearing before verbatim root `AGENTS.md` content;
- context-only Codex output when root `AGENTS.md` is absent or unreadable;
- Claude remaining context-only;
- preservation and refresh ownership rules for the new Codex path;
- private Git ignore behavior and clean Git status for
  `AGENTS.override.md`;
- launch-path coverage showing that an available configured Codex-family target
  receives the composed override.

The affected Go workspace and server suites provide the final verification.
