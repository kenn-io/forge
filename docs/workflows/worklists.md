# Work through a worklist

A worklist is a ranked list of pull requests and issues that an agent builds
for you in Kenn Forge. Items can come from any tracked repository and
provider. Open **Worklists** in the header to work through one item at a time.

## Ask an agent for a worklist

Connect an agent to the [MCP companion](../kenn-forge-mcp.md#build-worklists)
and describe what you want, for example "pull requests that are ready to merge
this week" or "issues that need a reply". The agent searches Forge's synced
data and writes the list.

Each item carries the agent's rank, an optional score from 1 to 10, a section
such as "Ready to merge", a note, and an optional caveat. A caveat is info,
warning, or critical. Warnings show in amber and critical caveats in red.

The list also keeps its recipe: the criteria and searches the agent used.
Expand **Recipe** in the list header to read it. To refresh a list, ask the
agent to run the recipe again. To get "another one like that", ask for a new
list from the same recipe.

## Work the list

The rail on the left shows the list's rows. Selecting a row opens the regular
pull request or issue detail beside it, with its conversation, workspace,
and, for a pull request, changed files.

- Press `j` and `k` to move between items, even with the rail collapsed.
- Press `d` to mark the open item handled and move to the next one. A handled
  row stays in place, dimmed, with a check.
- Use **Mark handled** in the item's context strip to toggle the mark.
- Filter rows by title, `owner/name#number`, author, label, section, note, or
  caveat. The search box uses the same syntax as the pull request list.
- Switch between **Open**, **Closed**, and **All**. Merged and closed items
  are hidden until you pick **Closed** or **All**.
- Switch between **Agent order**, which groups rows by section in the agent's
  rank, and **Score**, which puts the highest score first.

The progress bar counts items that are handled, merged, or closed. With more
than one list, the title is a picker that shows how many items each list has
left.

Shift- or Cmd/Ctrl-click rows to select several. **Actions** opens the same
menu as the pull request and issue lists: copy links, star, edit labels or
assignees, merge, close, reopen, or run a quick action, such as triage, on
each item in its own workspace. **Mark handled** marks the whole selection.

Forge remembers the list and item you had open. Reopening **Worklists** returns
you there.

## See what changed since the agent wrote the list

Forge records each item's state when the agent writes it: head commit,
comment count, CI result, review decision, and whether it was open. Rows then
name what has changed since, such as new commits, a new CI result, or a
merge. **N changed since** in the list header filters to those rows.

When an item has new commits, its note may describe an older head. The context
strip says so next to the note. Items merged or closed since the list was
written stay on the list until the agent rewrites it.

## Share or dismiss a list

Open a worklist link to create its list in your Forge. Opening the same link
again returns the same list. Forge skips items from repositories you don't
track, items it hasn't synced, and duplicates, and tells you how many it left
out.

Agents can hand you a link, or import one through the MCP companion. In
applications that support Kenn links, the list header also offers **Copy
link**. Handled marks stay with your copy and are not part of the link.

**Dismiss** hides a list. Use **Undo** right after if you change your mind.
A list also comes back when an agent rewrites it.
