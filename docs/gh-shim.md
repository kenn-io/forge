# GitHub CLI shim

Coding agents often run the same `gh pr list` and `gh pr view` commands many
times per session. Each call costs a GitHub API request and adds network
latency. The `kenn-forge` binary can stand in for `gh`. It answers supported
pull request queries from data Kenn Forge has already synced and passes every
other command to the real `gh` unchanged. No separate binary is needed: a `gh`
link to `kenn-forge` is the whole shim.

You choose which tools use it. Tools that do not have the shim on their `PATH`
keep calling `gh` directly.

## Before you start

- The Forge daemon is running on the same machine, as a standalone daemon, a
  fleet hub, or a spoke.
- The repositories you want served are configured in Forge and have finished
  at least one sync.
- The real [GitHub CLI](https://cli.github.com/) is installed and logged in.
  The shim never reads or forwards `gh` credentials. Commands it cannot answer
  run through `gh` with your normal login.

The shim serves GitHub repositories only. Calls for GitLab, Forgejo, and Gitea
hosts go straight to `gh`.

## Let an agent set it up

The quickest setup is to give this prompt to a coding agent running on the
same machine. It follows the manual steps on this page and checks the result.

```text
Set up the Kenn Forge GitHub CLI shim on this machine.
Follow https://forge.kenn.io/docs/gh-shim.md exactly.

1. Confirm that kenn-forge is on PATH, the Forge daemon is running, and
   the real gh is installed and logged in. Stop and tell me if any of
   these is missing.
2. Create the shim directory and gh link as described in the Install
   section.
3. Ask me which agent tools should use the shim. For each tool, change
   only how that tool is launched (a shell alias or a wrapper script)
   so the shim directory comes first on its PATH. Do not add the shim
   directory to my login shell's PATH.
4. Check the setup: from a checkout of a repository that Forge syncs,
   run a supported query through the shim with piped output, then show
   me the last usage-log entry. If the reason is not "served", explain
   it with the reason table on the docs page.
5. Tell me what you changed and how to undo it.
```

## Install

Create a directory that holds only a `gh` link to `kenn-forge`. When
`kenn-forge` runs under the name `gh`, it acts as the shim. Do not put this
directory on your login shell's `PATH`. Add it only for the tools that should
use Forge:

```sh
mkdir -p ~/.kenn/forge/gh-shim
ln -sf "$(command -v kenn-forge)" ~/.kenn/forge/gh-shim/gh
```

On Windows, create a hard link or copy of `kenn-forge.exe` named `gh.exe` in
that directory.

A `gh` script that runs `kenn-forge gh "$@"` works too. The shim notices when
the real `gh` it picked turns out to be that script, and skips it.

You can also run the shim directly as `kenn-forge gh <gh arguments>`, for
example `kenn-forge gh pr list --json number | cat`.

## Opt a tool in

Put the shim directory first on the tool's `PATH`. The real `gh` must stay
later on the same `PATH`:

```sh
PATH="$HOME/.kenn/forge/gh-shim:$PATH" claude
```

Any process started this way, including its subprocesses, gets the shim when it
runs `gh`. To stop using it, start the tool without the extra `PATH` entry.

The shim finds the real `gh` by searching `PATH` and skipping itself. If `gh`
is somewhere else, point to it directly:

```sh
export FORGE_GH_REAL=/opt/gh/bin/gh
```

## Check that it works

From a checkout of a configured repository, pipe a supported query:

```sh
gh pr list --json number,title --limit 5 | cat
```

Then check the most recent entry in the usage log:

```sh
tail -n 1 ~/.kenn/forge/gh-shim-usage.jsonl
```

`"reason":"served"` means Forge answered the query. Any other reason means the
call went to `gh`. See [Why a call was not served](#why-a-call-was-not-served).

## What Forge answers

Forge answers a call only when every part of it is supported. If any flag,
field, or argument is unsupported, the whole command goes to `gh`. Forge never
returns partial results.

| Command                           | Supported flags                                                     |
| --------------------------------- | ------------------------------------------------------------------- |
| `gh pr list` (or `gh pr ls`)      | `--json`, `--state`, `--head`, `--base`, `--limit`, `--repo`        |
| `gh pr view <number>`             | `--json`, `--repo`                                                  |

- `--json` is required. Supported fields are `number`, `title`, `state`, `url`,
  `body`, `isDraft`, `headRefName`, `headRefOid`, `baseRefName`, `createdAt`,
  `updatedAt`, `closedAt`, and `mergedAt`.
- `--state` accepts `open`, `closed`, `merged`, or `all`. `--limit` accepts 1
  to 1000 and defaults to 30, as in `gh`.
- Output must go to a pipe or file. Terminal output, `GH_FORCE_TTY`, and
  `CLICOLOR_FORCE` go to `gh`.
- `--jq`, `--template`, `--web`, branch names or URLs in `gh pr view`,
  `gh pr checks`, and every other command go to `gh`.

The JSON matches `gh` byte for byte: sorted keys, `null` for unset dates, and a
trailing newline.

## Repository detection

The shim identifies the repository from:

1. `--repo` or `-R`, as `owner/name`, `host/owner/name`, or a URL.
2. `GH_REPO`.
3. The checkout's Git remote, when it has exactly one.

With several remotes, `gh` applies its own rules to choose one, so the shim
passes the call through. Set `--repo` or `GH_REPO` in those checkouts. The host defaults to
`GH_HOST`, then the only host in your `gh` login config, then `github.com`.

## Freshness

The shim returns what Forge already has. It does not fetch from GitHub or
keep its own cache. Results are as current as Forge's normal sync.

- Open pull request lists need a completed repository sync with no sync error.
- `--state closed`, `merged`, or `all` also need a finished full archive for
  the repository, so the list includes every historical pull request. Start
  one with `kenn-forge archive start --repo 'github|github.com/owner/repo'`.
  See [Archive](archive.md).
- `gh pr view <number>` works for any pull request Forge has stored. On a spoke,
  a number the spoke has not stored is read from the hub.

If a requested field is missing for any pull request in the result, the whole
call goes to `gh`.

## Why a call was not served

Each call adds one line to `gh-shim-usage.jsonl` in Forge's home directory
(`~/.kenn/forge`, or `KENN_FORGE_HOME` when set). Each entry records the
time, the command, the full argument list including flag values, and a reason:

| Reason                  | Meaning                                                               |
| ----------------------- | --------------------------------------------------------------------- |
| `served`                | Forge answered the call                                               |
| `unsupported`           | The command, a flag, a field, or terminal output is not supported     |
| `repository_unresolved` | No `--repo` or `GH_REPO`, and the checkout does not have exactly one remote |
| `daemon_unavailable`    | The shim could not reach the Forge daemon                             |
| `untracked`             | The repository is not configured in Forge                             |
| `provider_unavailable`  | Forge could not load its configured repository list                   |
| `data_unavailable`      | Sync, the full archive, or a requested field is not complete yet      |

To see which calls happen most and whether Forge answered them:

```sh
jq -s 'group_by([.argv,.reason]) | map({argv: .[0].argv, reason: .[0].reason, count: length}) | sort_by(-.count)' ~/.kenn/forge/gh-shim-usage.jsonl
```

The log stays on your machine. It is never sent to the daemon or GitHub.
Delete it whenever you want.

The shim reads the default Forge config to find the daemon. Set
`FORGE_GH_CONFIG` to use a different config file.
