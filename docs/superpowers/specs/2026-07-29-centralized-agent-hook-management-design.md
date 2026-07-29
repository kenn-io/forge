# Centralized Agent Hook Management

## Goal

Adopt `go.kenn.io/kit` v0.14.0 as Middleman's single source of truth for
coding-agent hook profiles, config mutation, payload normalization, and native
response encoding. A default `middleman agent-hook install` or `uninstall`
operates on all eight kit profiles: Claude Code, Codex, GitHub Copilot CLI,
Cursor, Factory Droid, Gemini CLI, Hermes Agent, and Qwen Code.

## Scope

This migration replaces Middleman's Claude/Codex-specific integration layer.
It does not change the daemon-owned activity state machine, enable agent consent
or auto-approval settings, or broaden generated workspace context beyond Claude
`SessionStart` hooks.

The `--agent` option remains available for selecting one integration. Its
accepted values expand to every agent recognized by `agenthook.ParseAgent`.

## Architecture

### Installation and removal

Middleman delegates profile discovery, config-path resolution, command quoting,
event-name translation, conservative config merging, symlink preservation, and
uninstall matching to `go.kenn.io/kit/agenthook`.

The installed command retains the stable Middleman ownership marker
`--source middleman-agent-activity`. Middleman supplies the executable,
arguments, marker, and a two-second timeout for every event supported by the
selected profile. Kit supplies each profile's supported event set and native
timeout units. Reinstallation replaces only Middleman-owned registrations;
uninstallation preserves unrelated hooks and configuration.

With no `--agent`, install and uninstall iterate `agenthook.Profiles()` in kit's
stable order. A selected agent affects only that profile. Install output uses
kit's display name and resolved config path. Codex retains its one-time `/hooks`
review reminder because kit writes registrations without granting consent.

### Hook receipt and normalization

The installed thin CLI calls `agenthook.Handle` with the selected agent. Kit
normalizes the native hook payload into its Claude-shaped typed event model and
encodes the returned typed output in the invoking agent's native response
format.

A Middleman relay handler embeds `agenthook.NoopHandler` and overrides every
supported lifecycle method. Each override forwards `CommonInput.Raw`, which is
the complete normalized payload, to the existing loopback daemon endpoint along
with the agent name and runtime-session key. This keeps the daemon API and
activity store agent-neutral while ensuring native event and tool names are
translated once by kit.

The daemon continues to own activity transitions. It returns generated
workspace context only for Claude `SessionStart`; the relay maps that response
to `agenthook.SessionStartOutput.AdditionalContext`. Other events and agents
return zero typed outputs, allowing kit to emit the correct observational
response for each harness.

## Error Handling and Safety

Hook execution remains fail-open for Middleman's observational integration.
Malformed flags, unsupported agents, payload normalization errors, daemon
discovery failures, request failures, non-success responses, oversized or
malformed daemon responses, and context decoding failures produce no hook
control output and must not block the coding agent.

Install and uninstall remain ordinary CLI operations and return actionable
errors. They continue to require an absolute Middleman data directory before
installing commands. Kit must never enable an agent's hook consent or
auto-approval option.

The loopback daemon remains the sole owner of persisted activity state. The
installed hook command contains the Middleman config path but no daemon token;
the thin client discovers current runtime metadata and authentication for each
event.

## Testing

Focused tests will prove the boundaries independently:

- installation defaults to all eight profiles, while `--agent` selects one;
- kit-generated config keeps Middleman's marker, command arguments, timeout,
  unrelated config, and symlink behavior;
- a non-Claude native payload is normalized before the daemon receives it;
- Claude `SessionStart` context is mapped through the typed handler and encoded
  in Claude's native response;
- malformed hook input and daemon failures remain fail-open;
- the server still records normalized events and generates context only for
  Claude sessions.

The affected Go package tests and the full short test suite provide the runtime
verification. No agent-launch end-to-end test is added because agent-process
invocation is external and the repository already tests config mutation, CLI
relay, and HTTP handling as separate seams.

## Documentation

User workflow documentation will list the supported agents, explain the
all-profile default and single-agent override, and retain the Codex consent
step. Durable workspace and testing context will identify kit as the owner of
agent profiles, config mutation, normalization, and native response encoding.
