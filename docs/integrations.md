# Integrations

Kenn Forge brings Roborev reviews, Kata tasks, and local Docs folders directly
beside your pull requests, issues, and workspaces. Roborev keeps review jobs,
Kata keeps task data, and Docs reads files already on disk.

## Review Roborev jobs

Roborev runs as a separate daemon, and Forge does not start it for you. By
default, Forge looks for it at `http://127.0.0.1:7373`. To use another endpoint
or enable managed-clone initialization, add this section to your configuration:

```toml
[roborev]
endpoint = "http://127.0.0.1:7373"
init_managed_clones = true
```

Restart Forge after changing the endpoint. The optional managed-clone setting
is off by default, and you can also toggle it live under **Settings →
Workspaces**. When enabled, it runs `roborev init
--no-daemon` for new, retried, or recovered Forge-managed workspaces. This
requires Roborev v0.65.0 or newer and a loopback HTTP endpoint. Remote Roborev
endpoints still work for the Reviews panel when managed-clone initialization is
off.

Initialization applies only to Forge's managed clones. A repository configured
with `worktree_base_path`, a source-browser clone, or another user-owned
checkout keeps its existing user-managed hooks. Forge keeps each worktree's
Roborev snapshot exclusion separate, so changing or removing one workspace does
not hide paths in its siblings. If hook setup fails, Forge records a warning
and restores the previous hooks, and the workspace still opens with its
terminal and agents available. To retry initialization separately, see
[Roborev troubleshooting](troubleshooting.md#the-roborev-daemon-is-not-reachable).

To inspect jobs, open **Reviews** in a pull request's workspace panel or a
local workspace. Forge loads jobs when you display the panel and scopes them to
the workspace's repository and branch. You can filter jobs by status or Git
ref, and the table displays the agent, status, verdict, elapsed time, cost, job
type, and queue time.

Select a job to open its review drawer. Alongside the review itself, the drawer
shows the job log, the submitted prompt, Roborev comments, token usage, and the
controls that apply to the job's current state. From there, you can cancel
queued or running work, rerun a job, close or reopen a review, add a comment,
and copy the review output.

Roborev still owns the jobs and review data. Forge routes these reads and
actions through its local server so your browser does not connect to the daemon
directly.

### See a pull request's review history

Click the **Roborev** chip next to CI on a pull request to open every Roborev
review of that pull request in one list, combining reviews from the roborev-ci
GitHub App and local reviews from this machine's daemon. Each row shows the
reviewed commit, verdict, and finding count, and marks the review **Current**
when it reviewed the head commit, **Superseded** when newer commits arrived, or
**Addressed** when the local review was closed. Select any row to read the
review.

Local reviews appear only when the pull request has a ready workspace on this
machine, because Roborev finds them by the workspace's path. Without a ready
workspace, the list shows only roborev-ci reviews.

### Filter by Roborev findings

In the Pulls and Activity filter menus, the **Roborev findings** row reads the
latest roborev-ci review of each pull request's head commit. Require it to find
pull requests whose review reported problems, or exclude it to keep only pull
requests whose review came back clean.

Excluded is not the plain opposite of required. If a pull request's head commit
has no finished roborev-ci review, because it was never reviewed or its review
is pending, running, failed, or skipped, it matches neither side. Local daemon
reviews do not count, because they are not tied to a pull request.

## Keep Git hooks in new workspaces

When Forge creates a new managed clone, it uses `init.templateDir` from the
daemon user's global Git configuration, including global includes and XDG
configuration. Git copies the template into the new bare clone, so ordinary
commits in its linked workspaces run the installed hooks.

If you use the [pre-commit framework](https://pre-commit.com/#pre-commit-init-templatedir),
run these commands as the OS user that runs Forge on each machine:

```sh
git config --global init.templateDir ~/.git-template
pre-commit init-templatedir ~/.git-template
```

Install pre-commit and the tools your checks need where workspace sessions can
run them. Forge does not install tools or detect `.pre-commit-config.yaml` to
install hooks; the template comes from your configuration, not the repository.

This setup applies only to managed clones created after you configure the
template. Existing clones keep their hooks, and you can install hooks manually
in an existing workspace when needed. Source-browser clones and repositories
using `worktree_base_path` do not use this setup. Changing the template does
not update clones already on disk, and Forge's internal hook-disabled Git
operations stay hook-disabled.

## Link Kata issues

Kata is contextual rather than a separate page in the main navigation. Pull
requests, provider issues, and local workspaces each have a **Kata** tab where
you can link an issue and read its current details. Remote fleet workspaces
hide Kata controls. To edit the task, click **Open in Kata**.

From the **New workspace** dialog, you can search a selected Kata daemon and
create or reopen the worktree for a task. Forge reads the daemon catalog from
`$KATA_HOME/config.toml`, or `~/.kata/config.toml` when `KATA_HOME` is unset.
The selected daemon must be connected and use a supported API schema.

Before Forge can create workspaces for a Kata project, the project needs a
repository mapping. Open **Settings → Kata mappings** to inspect automatic
matches and add an override if a project points at the wrong repository. A
manual mapping looks like this:

```toml
[[kata_projects]]
daemon_id = "kata-main"
project_uid = "widgets"
provider = "github"
platform_host = "github.com"
repo_path = "acme/widgets"
```

The daemon ID is optional in a manual mapping. Omit it only when the same Kata
project should resolve to that repository for every configured daemon.

## Work with local Docs folders

Docs reads Markdown from folders you register, leaving the files on disk in
those folders. To get started:

1. Enable Docs mode in your Forge configuration:

   ```toml
   [modes]
   docs = true
   ```

2. Register at least one folder:

   ```sh
   kenn-forge docs add-folder --name Notes ~/notes
   ```

From the Docs page, you can browse, search, edit, pull, and publish files. If a
folder's task references belong to one Kata daemon, pin that daemon when you
register the folder:

```sh
kenn-forge docs add-folder --id project --daemon kata-main ~/project-docs
```

That binding lets task references open in Kata without copying Kata task data
into the Markdown folder.

For details on the folder tree, search, editing, and Git workflow, see
[Read and edit local Docs](workflows/docs.md).
