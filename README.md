<h1 align="center">kenn-forge</h1>

<p align="center">
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.27.4+-00ADD8?logo=go" alt="Go 1.27.4+"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Elastic_2.0-yellow.svg" alt="License: Elastic 2.0"></a>
  <a href="https://forge.kenn.io"><img src="https://img.shields.io/badge/Docs-forge.kenn.io-blue" alt="Docs"></a>
</p>

<p align="center">
  <a href="https://forge.kenn.io">Documentation</a> ·
  <a href="docs/quickstart.md">Quick Start</a> ·
  <a href="docs/configuration.md">Configuration</a>
</p>

**A local maintainer console for pull requests, issues, reviews, activity,
and workspaces.**

kenn-forge syncs the repositories you maintain into a local SQLite database
and serves the UI from one binary.

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

## Installation

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
| Windows | ARM64 | `forge_<version>_windows_arm64.zip` |
| Android | Universal | `forge_<version>_android.apk` |

On Linux, macOS, and Windows, extract the archive and move `kenn-forge` or
`kenn-forge.exe` to a directory on your `PATH`.

The Android APK installs a client app for phones and tablets that connects to a
Forge server you already run. Install the APK, open the app, and enter the
server's full URL and its auth token if the server requires one.

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

For Docker or Umbrel, use the [local image and startup settings](docs/configuration.md#container-startup).

Local workspaces require Git and tmux on a Unix-like host. The Windows release
supports the dashboard and provider actions. Use WSL or a remote Unix-like
kenn-forge host when you need workspace sessions.

## GitHub CLI shim

A `gh` symlink to `kenn-forge` (or `kenn-forge gh ...`) is an opt-in stand-in
for `gh` that answers piped `gh pr list` and `gh pr view <number>` JSON queries
from synced Forge data and passes everything else to the real `gh`. See the [GitHub CLI shim guide](docs/gh-shim.md) for
setup, supported queries, and debug logging.

## Documentation

- [Quick Start](docs/quickstart.md)
- [Workflows](docs/workflows.md)
- [Integrations](docs/integrations.md)
- [Configuration](docs/configuration.md)
- [Commands](docs/commands.md)
- [MCP companion](docs/kenn-forge-mcp.md)
- [GitHub CLI shim](docs/gh-shim.md)
- [Troubleshooting](docs/troubleshooting.md)

kenn-forge is licensed under the [Elastic License 2.0](LICENSE). Contributions
made before the relicense remain available under the MIT License; see
[NOTICE](NOTICE). Contact [Kenn Software](https://kenn.io) at info@kenn.io for
commercial licensing.
