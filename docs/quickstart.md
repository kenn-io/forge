# Quick Start

Install Kenn Forge, connect a code forge, and open your first workspace.

## Install a release

Download the archive for your system from
[GitHub Releases](https://github.com/kenn-io/middleman/releases). Each release
also includes `SHA256SUMS`.

| System | Architecture | Archive |
| --- | --- | --- |
| Linux | x86-64 | `forge_<version>_linux_amd64.tar.gz` |
| Linux | ARM64 | `forge_<version>_linux_arm64.tar.gz` |
| macOS | Intel | `forge_<version>_darwin_amd64.tar.gz` |
| macOS | Apple silicon | `forge_<version>_darwin_arm64.tar.gz` |
| Windows | x86-64 | `forge_<version>_windows_amd64.zip` |

Verify the downloaded archive against `SHA256SUMS`.

Linux:

```sh
sha256sum -c SHA256SUMS --ignore-missing
```

macOS:

```sh
shasum -a 256 forge_<version>_darwin_<arch>.tar.gz
grep 'forge_<version>_darwin_<arch>.tar.gz' SHA256SUMS
```

Windows PowerShell:

```powershell
Get-FileHash .\forge_<version>_windows_amd64.zip -Algorithm SHA256
Select-String -Path .\SHA256SUMS -Pattern 'forge_<version>_windows_amd64.zip'
```

Compare the printed SHA-256 values before extracting the archive.

Extract the archive. Move `kenn-forge` or `kenn-forge.exe` to a directory on
your `PATH`.

If the Releases page has no published version, build from source.

## Build from source

Source builds require Go 1.26+ and [Bun](https://bun.sh/).

```sh
git clone https://github.com/kenn-io/middleman.git kenn-forge
cd kenn-forge
make build
```

The build embeds the frontend in `./kenn-forge`. Run `make install` to install
an optimized binary.

## Start Kenn Forge

The commands below assume `kenn-forge` is on `PATH`. After `make build`, use
`./kenn-forge` instead or run `make install`.

GitHub users get the shortest setup path with an authenticated GitHub CLI:

```sh
gh auth login
kenn-forge
```

You can also provide a GitHub token directly:

```sh
export KENN_FORGE_GITHUB_TOKEN=ghp_your_token_here
kenn-forge
```

In PowerShell:

```powershell
$env:KENN_FORGE_GITHUB_TOKEN = 'ghp_your_token_here'
.\kenn-forge.exe
```

For another provider or host, set its token environment variable before
starting. To use `token_env` or `token_file`, start once to create
`~/.kenn/forge/config.toml`, edit it, then restart. Settings chooses provider
hosts and repository patterns, but it does not store credentials. See
[Configuration](configuration.md#credentials).

Open `http://127.0.0.1:8091`. Kenn Forge creates
`~/.kenn/forge/config.toml` on first run.

## Complete first-run setup

The setup flow leads to a synced pull request and, on a host with Git and tmux,
a working local workspace:

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="assets/generated/first-run-light.svg" alt="Kenn Forge code forge readiness step in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="assets/generated/first-run-dark.svg" alt="Kenn Forge code forge readiness step in dark mode">
  <figcaption>First run checks the GitHub CLI and routes other providers through repository setup.</figcaption>
</figure>

1. **Connect a code forge.** Continue with an authenticated GitHub CLI, or
   open Settings for another provider or host.
2. **Choose repositories.** GitHub users can select discovered repositories.
   Other providers use the Repositories panel in Settings.
3. **Run the first sync.** Kenn Forge loads pull requests, issues, and activity
   for the configured repositories.
4. **Open a pull request.** Choose an open item from the synced list.
5. **Start a workspace.** Create a local worktree and open its working session.

You can leave setup and return later. Kenn Forge resumes unfinished setup after
you return to a provider view.

If GitHub discovery cannot find what you need, open **Settings → Repositories**.
For another provider, choose its host and repository pattern there after its
credential is available to the daemon.

The Windows release supports the dashboard and provider actions. Local
workspace sessions require Git and tmux on a Unix-like host. Use WSL or a remote
Unix-like Kenn Forge host for that step.

## Use the main views

- **Activity**: scan recent cross-repository changes.
- **Pulls**: review discussion, diffs, CI, and merge state.
- **Issues**: triage, comment, change state, or create a workspace.
- **Repos**: browse configured source and branches.
- **Workspaces**: open local shells and configured agents.
- **Settings**: manage repositories, agents, and app preferences.

Press `?` to see shortcuts for the current view.

## Enable optional modes

Kata and Docs are hidden by default:

```toml
[modes]
kata = true
docs = true
```

Kata reads daemon definitions from Kata's config. Docs uses folders registered
with `kenn-forge docs add-folder`.

Continue with [daily workflows](workflows.md) or the
[configuration reference](configuration.md).
