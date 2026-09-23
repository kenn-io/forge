# kenn-forge

kenn-forge is a local maintainer console for pull requests, issues, reviews,
activity, and local workspaces. It syncs the repositories you maintain into a
local SQLite database and serves the UI from one binary.

Start with the [user guide](docs/index.md) for setup and workflows.

## Teaser

<picture>
  <source
    media="(prefers-color-scheme: dark)"
    srcset="https://forge.kenn.io/docs/assets/generated/maintainer-overview-dark.svg"
  >
  <source
    media="(prefers-color-scheme: light)"
    srcset="https://forge.kenn.io/docs/assets/generated/maintainer-overview-light.svg"
  >
  <img
    src="https://forge.kenn.io/docs/assets/generated/maintainer-overview-light.svg"
    alt="kenn-forge Activity with a selected pull request, live workspace, and coding-agent session"
  >
</picture>

<sub>Activity keeps the selected pull request beside its live workspace and coding-agent session.</sub>

## What you can do

- Triage activity across repositories and provider hosts.
- Review pull requests and merge requests with discussion, diffs, and CI context.
- Work with issues without leaving the console.
- Create local workspaces for hands-on review or implementation.
- Read and respond to Roborev reviews from a running daemon.
- Link Kata issues to provider work and local workspaces.
- Browse and edit registered Markdown folders.
- Expose cached maintainer workflows to local MCP clients from the daemon.
- View and operate remote kenn-forge daemons through a federated fleet.

Provider capabilities vary. kenn-forge shows unsupported actions as unavailable.

## Install a release

Download the archive for your system from
[GitHub Releases](https://github.com/kenn-io/forge/releases). Releases
include a `SHA256SUMS` file.

| System | Architecture | Archive |
| --- | --- | --- |
| Linux | x86-64 | `forge_<version>_linux_amd64.tar.gz` |
| Linux | ARM64 | `forge_<version>_linux_arm64.tar.gz` |
| macOS | Intel | `forge_<version>_darwin_amd64.tar.gz` |
| macOS | Apple silicon | `forge_<version>_darwin_arm64.tar.gz` |
| Windows | x86-64 | `forge_<version>_windows_amd64.zip` |

Extract the archive and move `kenn-forge` or `kenn-forge.exe` to a directory on
your `PATH`.

Build the current source when no release is published:

```sh
git clone https://github.com/kenn-io/forge.git kenn-forge
cd kenn-forge
make install
```

Source builds require Go 1.27+ and [Bun](https://bun.sh/).

## Start

GitHub users can reuse an authenticated GitHub CLI session:

```sh
gh auth login
kenn-forge daemon start
```

Open `http://127.0.0.1:8091`. First-run setup connects a code forge, adds
repositories, runs the first sync, and opens a pull request.

Use `kenn-forge serve` when you want the server attached to the foreground for
development or diagnosis.

The Repositories panel selects provider hosts and repository patterns. GitLab,
Forgejo, and Gitea hosts reuse an authenticated `glab` or `fj` session the same
way. Configure explicit credentials through environment variables or
`~/.kenn/forge/config.toml`; see [Configuration](docs/configuration.md).

Local workspaces require Git and tmux on a Unix-like host. The Windows release
supports the dashboard and provider actions. Use WSL or a remote Unix-like
kenn-forge host when you need workspace sessions.

## GitHub CLI shim

Build `forge-gh` with `go build -o tmp/forge-gh ./cmd/forge-gh`. Keep the real
`gh` installed. To opt a tool into the shim, put a symlink named `gh` to this
binary in a separate directory ahead of the real `gh` on that tool's `PATH`.
`FORGE_GH_REAL` can select the real executable explicitly.

The shim serves piped `pr list` and `pr view <number>` JSON queries for watched
GitHub repositories from Forge's SQLite data through the local daemon. Normal
Forge sync owns freshness; the shim neither fetches provider data nor keeps an
extra cache. List queries require a completed repository sync, and historical
lists also require a complete archived PR inventory. List filters include
`--head`, `--base`, `--state`, and `--limit`. Supported JSON fields
are `number,title,state,url,body,isDraft,headRefName,headRefOid,baseRefName,createdAt,updatedAt,closedAt,mergedAt`.

Everything else delegates unchanged to `gh`, including untracked repositories,
branch-based views, checks, terminal output, `--jq`, `--template`, unsupported
fields, missing or incomplete stored data, and unavailable daemons. Use `--repo`
or `GH_REPO` when the checkout has multiple remotes. `FORGE_GH_CONFIG` selects a non-default Forge config file.

Command names and outcomes are appended to `forge-gh-usage.jsonl` in Forge's
config directory. Argument values and result data are not recorded. To count
which commands are served or delegated:

```sh
jq -s 'group_by([.command,.reason]) | map({command: .[0].command, reason: .[0].reason, count: length})' ~/.kenn/forge/forge-gh-usage.jsonl
```

## Documentation

- [Quick Start](docs/quickstart.md)
- [Workflows](docs/workflows.md)
- [Integrations](docs/integrations.md)
- [Configuration](docs/configuration.md)
- [Commands](docs/commands.md)
- [MCP companion](docs/kenn-forge-mcp.md)
- [Troubleshooting](docs/troubleshooting.md)

kenn-forge is licensed under the [Elastic License 2.0](LICENSE). Contributions
made before the relicense remain available under the MIT License; see
[NOTICE](NOTICE). Contact [Kenn Software](https://kenn.io) at info@kenn.io for
commercial licensing.
