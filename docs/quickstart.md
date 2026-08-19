# Quick start

Install kenn-forge, connect a code forge, and open your first workspace.

## Install a release

<div class="docs-actions">
  <a class="md-button md-button--primary" data-download-current href="https://github.com/kenn-io/forge/releases">Download latest release</a>
  <a class="md-button" href="https://github.com/kenn-io/forge/releases">All releases</a>
</div>

Download the archive for your system below or browse
[GitHub Releases](https://github.com/kenn-io/forge/releases). Each release
also includes <a data-download-asset="checksums" href="https://github.com/kenn-io/forge/releases"><code>SHA256SUMS</code></a>.

| System  | Architecture  | Archive                                                                                                                                             |
| ------- | ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Linux   | x86-64        | <a data-download-asset="linux-amd64" href="https://github.com/kenn-io/forge/releases"><code>forge\_&lt;version&gt;\_linux_amd64.tar.gz</code></a>   |
| Linux   | ARM64         | <a data-download-asset="linux-arm64" href="https://github.com/kenn-io/forge/releases"><code>forge\_&lt;version&gt;\_linux_arm64.tar.gz</code></a>   |
| macOS   | Intel         | <a data-download-asset="darwin-amd64" href="https://github.com/kenn-io/forge/releases"><code>forge\_&lt;version&gt;\_darwin_amd64.tar.gz</code></a> |
| macOS   | Apple silicon | <a data-download-asset="darwin-arm64" href="https://github.com/kenn-io/forge/releases"><code>forge\_&lt;version&gt;\_darwin_arm64.tar.gz</code></a> |
| Windows | x86-64        | <a data-download-asset="windows-amd64" href="https://github.com/kenn-io/forge/releases"><code>forge\_&lt;version&gt;\_windows_amd64.zip</code></a>  |

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
git clone https://github.com/kenn-io/forge.git kenn-forge
cd kenn-forge
make build
```

The build embeds the frontend in `./kenn-forge`. Run `make install` to install
an optimized binary.

## Start kenn-forge

The commands below assume `kenn-forge` is on `PATH`. After `make build`, use
`./kenn-forge` instead or run `make install`.

GitHub users get the shortest setup path with an authenticated GitHub CLI:

```sh
gh auth login
kenn-forge daemon start
```

Use `kenn-forge serve` instead when you want foreground logs for development or
diagnosis.

Open `http://127.0.0.1:8091`. kenn-forge creates
`~/.kenn/forge/config.toml` on first run.

## Complete first-run setup

The setup flow leads to a synced pull request and, on a host with Git and tmux,
a working local workspace:

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="assets/generated/first-run-light.svg" alt="kenn-forge code forge readiness step in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="assets/generated/first-run-dark.svg" alt="kenn-forge code forge readiness step in dark mode">
  <figcaption>First run checks the GitHub CLI and routes other providers through repository setup.</figcaption>
</figure>

1. **Connect a code forge.** Continue with an authenticated GitHub CLI, or
   open Settings for another provider or host.
2. **Choose repositories.** GitHub users can select discovered repositories.
   Other providers use the Repositories panel in Settings.
3. **Run the first sync.** kenn-forge loads pull requests, issues, and activity
   for the configured repositories.
4. **Open a pull request.** Choose an open item from the synced list.
5. **Start a workspace.** Create a local worktree and open its working session.

You can leave setup and return later. kenn-forge resumes unfinished setup after
you return to a provider view.

If GitHub discovery cannot find what you need, open **Settings → Repositories**.
For another provider, choose its host and repository pattern there after its
credential is available to the daemon.

The Windows release supports the dashboard and provider actions. Local
workspace sessions require Git and tmux on a Unix-like host. Use WSL or a remote
Unix-like kenn-forge host for that step.

## Use the main views

- [**Activity**](workflows/activity.md): scan recent cross-repository changes.
- [**Pulls**](workflows/code-reviewer.md): review discussion, diffs, CI, and merge state.
- [**Issues**](workflows/issue-triager.md): triage, comment, change state, or create a workspace.
- [**Repos**](workflows/repositories.md): browse configured source, refs, and file history.
- [**Workspaces**](workflows/workspaces.md): open local shells and configured agents.
- [**Docs**](workflows/docs.md): read and edit registered Markdown folders.
- [**Settings**](settings.md): manage repositories, agents, modes, and app preferences.

Press `?` to see shortcuts for the current view.

On a phone, open `/m` for Activity, Pulls, Issues, and Workspaces in a
touch-first layout. Docs and Kata-linked task detail remain desktop-first.

## Advanced credential setup

Most GitHub users do not need this section. Use it when the GitHub CLI is not
available, or when you connect another provider or host.

To provide a GitHub token directly:

```sh
export KENN_FORGE_GITHUB_TOKEN=ghp_your_token_here
kenn-forge daemon start
```

In PowerShell:

```powershell
$env:KENN_FORGE_GITHUB_TOKEN = 'ghp_your_token_here'
kenn-forge.exe daemon start
```

For another provider or host, set its token environment variable before
starting. To use `token_env` or `token_file`, start once to create
`~/.kenn/forge/config.toml`, edit it, then restart. Settings chooses provider
hosts and repository patterns, but it does not store credentials. See
[Configuration](configuration.md#credentials).

## Connect optional integrations

Docs is hidden until you register a local Markdown folder:

```toml
[modes]
docs = true
```

```sh
kenn-forge docs add-folder --name Notes ~/notes
```

The **Reviews** page connects to a Roborev daemon. The default endpoint is
`http://127.0.0.1:7373`. Kata has no top-level mode. Once kenn-forge finds a
Kata daemon in `$KATA_HOME/config.toml` or `~/.kata/config.toml`, you can link
Kata issues from pull requests, provider issues, and local workspaces. Remote
fleet workspaces do not show Kata controls. You can also choose a Kata issue in
the **New workspace** dialog.

See [Integrations](integrations.md) for Roborev endpoints, Kata repository
mappings, and Docs folder bindings.

Continue with [daily workflows](workflows.md) or the
[configuration reference](configuration.md).
