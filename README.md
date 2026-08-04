# kenn-forge

kenn-forge is a local maintainer console for pull requests, issues, reviews,
activity, and local workspaces. It syncs the repositories you maintain into a
local SQLite database and serves the UI from one binary.

Start with the [user guide](docs/index.md) for setup and workflows.

## What you can do

- Triage activity across repositories and provider hosts.
- Review pull requests and merge requests with discussion, diffs, and CI context.
- Work with issues without leaving the console.
- Create local workspaces for hands-on review or implementation.
- Connect optional Kata task daemons and local Markdown folders.
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

Source builds require Go 1.26+ and [Bun](https://bun.sh/).

## Start

GitHub users can reuse an authenticated GitHub CLI session:

```sh
gh auth login
kenn-forge
```

Open `http://127.0.0.1:8091`. First-run setup connects a code forge, adds
repositories, runs the first sync, and opens a pull request.

The Repositories panel selects provider hosts and repository patterns. Configure
non-GitHub and explicit credentials through environment variables or
`~/.kenn/forge/config.toml`; see [Configuration](docs/configuration.md).

Local workspaces require Git and tmux on a Unix-like host. The Windows release
supports the dashboard and provider actions. Use WSL or a remote Unix-like Kenn
Forge host when you need workspace sessions.

## Documentation

- [Quick Start](docs/quickstart.md)
- [Workflows](docs/workflows.md)
- [Configuration](docs/configuration.md)
- [Commands](docs/commands.md)
- [Troubleshooting](docs/troubleshooting.md)

kenn-forge is licensed under the [Elastic License 2.0](LICENSE). Contributions
made before the relicense remain available under the MIT License; see
[NOTICE](NOTICE). Contact [Kenn Software](https://kenn.io) at info@kenn.io for
commercial licensing.
