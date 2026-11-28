# Follow activity across repositories

When you maintain more than one repository, Kenn Forge's **Activity** view is
the best place to start. It combines recent pull requests, issues, commits,
reviews, and comments in one queue.

## Choose the right view

Use **Threaded** mode to group events under their pull request or issue. This
is usually quieter because a busy discussion takes one row. Switch to **Flat**
mode when exact event order matters.

Threaded Activity loads older pull requests and issues as you scroll, and Flat
Activity loads up to 5,000 events as you scroll. If a request fails, select
**Retry** when it is available. Changing the time range, repository, or search
starts a new list, and Activity updates refresh the pages you have loaded.
Newly active items can push older items onto the next page.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/activity-threaded-light.svg" alt="Forge Activity in Threaded mode with events grouped under their pull requests and issues in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/activity-threaded-dark.svg" alt="Forge Activity in Threaded mode with events grouped under their pull requests and issues in dark mode">
  <figcaption>Threaded Activity groups each pull request's or issue's events under one row, by repository.</figcaption>
</figure>

You can filter the queue by:

- Time range and repository
- Pull requests, issues, and default-branch commits
- Event type, author, labels, or text
- Open or closed state
- Human or bot activity
- The pull request conditions from the **PRs** filter menu: draft, approved,
  merge conflicts, merge queued, CI, has a workspace, from a fork, and
  [Roborev findings](../integrations.md#filter-by-roborev-findings)

The Activity filter menu uses the same names and order as the **PRs** filter
menu, and each condition cycles through off, required, and excluded. Pull
request conditions apply only to pull request activity; issue activity stays in
the queue.

On GitHub, each pull request commit row shows its original author.
Default-branch rows use the author from the most recently synced pull request
with the same repository and full SHA. Text search includes those displayed
names. Other providers keep their existing author names.

Text search matches every word you type. Put `!` or `NOT` before a word to
exclude it. For example, `fix NOT alice` finds items matching "fix" and hides
anything matching "alice", including items alice opened. Quote a phrase to keep
its words together, as in `!"needs review"`. The search box highlights `!` and
`NOT` when they act as operators. The **PRs**, **Issues**, and **Workspaces**
search boxes parse queries the same way.

To set your defaults, open **Settings → Activity** and choose a starting time
range, item and event types, visibility filters, and commit rollup. Forge saves
these defaults in `config.toml` and applies them when you open Activity.

Your filter choices stay in the Activity URL. You can bookmark a useful queue
or share the link with another Forge user who has access to the same
repositories. Refreshing the page preserves the selected time range and the
**Open** state choice, even when they differ from the defaults in **Settings**.

## Read an item without losing the queue

Select a pull request or issue to open its detail beside Activity. You get the
same conversation, files, and workspace actions as in the dedicated **PRs** or
**Issues** view.

Default-branch commit events open a commit diff. Use the file tree to move
through the change, then close the detail to return to the same spot in the
queue.

If the selected item has a workspace, its live shell or agent sessions can sit
beside the conversation and files. Resize or collapse the Activity rail when
you need more room.

## Include local work when it matters

By default, Activity follows provider events. If you want a shell or agent
session to move its pull request or issue back into the recent queue, turn on
workspace activity in **Settings → Activity**.

Leave it off when you want the queue to reflect only what changed at the
provider.

## Refresh one repository

When the selected repository is stale, choose **Sync current repo** to update
it without scheduling a full sync of every configured repository.

The global repository selector also controls Activity scope. A named repository
set is handy for keeping a team, service group, or release train together.

To start a set from a GitHub team, open the selector, choose **Select from
GitHub team…**, then pick one of your configured GitHub organizations and a
team. Forge replaces the current selection with every configured repository
the team can access, read-only access included. Adjust the selection if you
like, then choose **Save preset**. The preset is a snapshot: it does not change
when the team gains or loses repositories later. If Forge can't load every
configured repository the team returned, it applies nothing and offers
**Retry** instead of a partial selection.

The organizations are the owners of your configured repositories. A
repository transferred to another organization stays under its configured
owner until you update its entry in Settings.

Team lookup uses the GitHub user credential configured for the organization,
or the host's default: a personal access token or a login from
`gh auth login`. A token set for a single repository is not used. Forge does not read teams through a
GitHub App installation, so an App-only configuration cannot use the picker.
GitHub returns only the teams and repositories that credential can see, so a
repository synced through another credential may be missing from the result.
A classic token needs the `read:org` scope; a fine-grained token needs
organization **Members** read access. An organization with no visible teams
may simply have none.
