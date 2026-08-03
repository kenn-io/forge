# kenn-forge

Kenn Forge is a local maintainer console for pull requests, issues, reviews,
activity, and local workspaces. It syncs the repositories you maintain into a
local SQLite database and serves the UI from one binary.

Start with the [user guide](docs/index.md) for setup and workflows.

## What you can do

- Triage activity across repositories and provider hosts.
- Review pull requests and merge requests with discussion, diffs, and CI context.
- Work with issues without leaving the console.
- Create local workspaces for hands-on review or implementation.
- Connect optional Kata task daemons and local Markdown folders.
- View and operate remote Kenn Forge daemons through a federated fleet.

Provider capabilities vary. Kenn Forge shows unsupported actions as unavailable.

## Install a release

Download the archive for your system from
[GitHub Releases](https://github.com/kenn-io/middleman/releases). Releases
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
git clone https://github.com/kenn-io/middleman.git kenn-forge
cd kenn-forge
make build
```

Source builds require Go 1.26+ and [Bun](https://bun.sh/). Run `make install`
to install an optimized build.

## Start

GitHub users can reuse an authenticated GitHub CLI session:

```sh
gh auth login
kenn-forge
```

Open `http://127.0.0.1:8091`. First-run setup connects a code forge, adds
repositories, runs the first sync, opens a pull request, and can create a local
workspace.

GitLab, Forgejo, Gitea, self-hosted providers, and explicit token setup use the
Repositories panel in Settings. Kenn Forge stores its config and data under
`~/.kenn/forge/` by default.

## Documentation

- [Quick Start](docs/quickstart.md)
- [Workflows](docs/workflows.md)
- [Configuration](docs/configuration.md)
- [Commands](docs/commands.md)
- [Troubleshooting](docs/troubleshooting.md)

Kenn Forge is licensed under the [Elastic License 2.0](LICENSE).
