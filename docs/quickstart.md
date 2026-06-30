# Quick start

## Requirements

- Go 1.26+
- Bun for frontend dependencies and builds
- A provider token, or `gh auth token` for GitHub

## Build

```sh
git clone https://github.com/kenn-io/middleman.git
cd middleman
make build
```

This builds a `middleman` binary with the frontend embedded.

## Start middleman

For GitHub, either authenticate with the GitHub CLI:

```sh
gh auth login
./middleman
```

or set a token explicitly:

```sh
export MIDDLEMAN_GITHUB_TOKEN=ghp_your_token_here
./middleman
```

On first run, middleman creates `~/.config/middleman/config.toml` and starts the
UI at:

```text
http://127.0.0.1:8091
```

Install the binary to your path when you want to run it normally:

```sh
make install
```

## Add repositories

Use Settings in the UI, or edit `~/.config/middleman/config.toml`:

```toml
[[repos]]
owner = "your-org"
name = "your-repo"

[[repos]]
owner = "your-org"
name = "another-repo"
```

Restart middleman after editing the config file. The first sync starts on
startup and then repeats on the configured interval.

## Open the main views

- **Activity**: recent cross-repo changes and discussion.
- **Pulls**: PR/MR triage, review, CI, diff, and merge workflows.
- **Issues**: issue triage, comments, state changes, and workspace launch.
- **Board**: local kanban state for pull requests.
- **Reviews**: review jobs and review-oriented activity.
- **Workspaces**: local working sessions tied to repos and tasks.
- **Settings**: repository and app configuration.

Press `?` in the UI for the current keyboard shortcuts.

## Optional modes

Kata, Docs, and Messages are hidden by default. Enable them in config:

```toml
[modes]
kata = true
docs = true
messages = true
```

- Kata reads daemon definitions from Kata's own config.
- Docs uses folders you register with `middleman docs add-folder`.
- Messages requires a msgvault URL and API key environment variable.
