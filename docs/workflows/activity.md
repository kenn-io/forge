# Follow activity across repositories

Activity is the best place to start when you maintain more than one repository.
It combines recent pull request, issue, commit, review, and comment activity in
one queue.

## Choose the right view

Use **Threaded** mode to group events under their pull request or issue. This is
usually the quieter view because a busy discussion takes one row. Use **Flat**
mode when exact event order matters.

The filters can narrow the queue by:

- time range and repository
- pull requests, issues, and default-branch commits
- event type, author, or text
- open or closed state
- human or bot activity

Filter choices stay in the Activity URL. You can bookmark a useful queue or
send the link to another kenn-forge user with access to the same repositories.

## Read an item without losing the queue

Select a pull request or issue to open its detail beside Activity. The detail
has the same conversation, files, and workspace actions as the dedicated Pulls
or Issues view.

Default-branch commit events open a commit diff. Use the file tree to move
through the change, then close the detail to return to the same place in the
queue.

If the selected item has a workspace, its live shell or agent sessions can sit
beside the conversation and files. Resize or collapse the Activity rail when
you need more room.

## Include local work when it matters

By default, Activity follows provider events. Turn on workspace activity in
**Settings → Activity** when a shell or agent session should move its pull
request or issue back into the recent queue.

This setting is useful when local work is the signal you care about. Leave it
off when the queue should reflect only what changed at the provider.

## Refresh one repository

Use **Sync current repository** when the selected repository is stale. This
updates that repository without scheduling a full sync of every configured
repository.

The global repository selector also controls Activity scope. A named repository
set is handy for keeping a team, service group, or release train together.
