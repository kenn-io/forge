# Integrations

Roborev keeps review jobs, Kata keeps task data, and Docs reads files already
on disk. kenn-forge puts each one beside the pull requests, issues, and
workspaces where you need it.

## Review Roborev jobs

Roborev runs as a separate daemon. kenn-forge looks for it at
`http://127.0.0.1:7373` unless you set another endpoint:

```toml
[roborev]
endpoint = "http://127.0.0.1:7373"
```

Restart kenn-forge after changing the endpoint. kenn-forge does not start the
Roborev daemon for you.

Open **Reviews** to see jobs from the connected daemon. Filter by repository,
branch, status, or Git ref. The table shows the agent, status, verdict, elapsed
time, cost, job type, and queue time.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="assets/generated/roborev-reviews-light.svg" alt="kenn-forge Reviews with a selected Roborev job in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="assets/generated/roborev-reviews-dark.svg" alt="kenn-forge Reviews with a selected Roborev job in dark mode">
  <figcaption>Select a Roborev job to read the review, inspect its log and prompt, and respond without leaving kenn-forge.</figcaption>
</figure>

Select a job to open its review. The drawer also has the job log, the submitted
prompt, Roborev comments, token usage, and controls that apply to the current
state. You can cancel queued or running work, rerun a job, close or reopen a
review, add a comment, and copy the review output.

Roborev still owns the jobs and review data. kenn-forge sends these reads and
actions through its local server so the browser does not connect to the daemon
directly. Set `reviews = false` under `[modes]` if you do not use Roborev.

## Link Kata issues

Kata is contextual. It does not add a Kata page to the main navigation.
Instead, pull requests, provider issues, and workspaces have a **Kata** tab
where you can link an issue and read its current details. Use **Open in Kata**
when you need to edit the task.

The **New workspace** dialog can search a selected Kata daemon and create or
reopen the worktree for a task. kenn-forge reads the daemon catalog from
`$KATA_HOME/config.toml`. Without `KATA_HOME`, it uses `~/.kata/config.toml`.
The selected daemon must be connected and use a supported API schema.

Kata projects need repository mappings before kenn-forge can create their
workspaces. Open **Settings → Kata mappings** to inspect automatic matches and
add an override when a project points at the wrong repository. A manual mapping
looks like this:

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

Docs reads Markdown from folders you register. The files stay on disk in those
folders. Enable the mode and register at least one folder:

```toml
[modes]
docs = true
```

```sh
kenn-forge docs add-folder --name Notes ~/notes
```

The Docs page can browse, search, edit, pull, and publish files. If a folder's
task references belong to one Kata daemon, pin it when you register the folder:

```sh
kenn-forge docs add-folder --id project --daemon kata-main ~/project-docs
```

That binding lets task references open in Kata. It does not copy Kata task data
into the Markdown folder.
