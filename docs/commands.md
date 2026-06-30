# Commands

## Serve the app

```sh
middleman
middleman serve
middleman serve -config /path/to/config.toml
```

Without a subcommand, `middleman` starts the daemon and web UI.

## Version

```sh
middleman version
```

Prints the version, commit, and build date.

## Status

```sh
middleman status
middleman status -json
middleman status -config /path/to/config.toml
```

Reports whether a middleman daemon is running.

## Config

```sh
middleman config read port
middleman config read -config /path/to/config.toml port
```

The current CLI exposes a small read surface. Use the Settings UI or edit the
TOML file for normal configuration changes.

## Docs folders

```sh
middleman docs list-folders
middleman docs add-folder --name Docs ~/docs
middleman docs add-folder --id project --daemon kata-main ~/project-docs
middleman docs remove-folder project
```

These commands manage `[[doc_folders]]` in the config file.

## GitHub App credentials

```sh
middleman-github-app create
middleman-github-app list
middleman-github-app install
middleman-github-app uninstall
middleman-github-app delete
middleman-github-app open
```

Use this companion CLI when you want middleman sync reads to use GitHub App
installation tokens instead of your personal access token rate limit.
