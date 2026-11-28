# Work in local sessions

A Kenn Forge workspace ties a tracked repository and Git worktree to the shell
and agent sessions doing the work. Create one from a pull request, provider
issue, linked Kata issue, or **New workspace**.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/workspace-codex-session-light.svg" alt="Forge Workspaces with a pull request worktree and running Codex session in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/workspace-codex-session-dark.svg" alt="Forge Workspaces with a pull request worktree and running Codex session in dark mode">
  <figcaption>A workspace keeps its branch, linked item, files, diff, and running sessions together.</figcaption>
</figure>

For remote compute, connect a [devbox](../devboxes.md) and select it as your
workspace machine. Agents, worktrees, and tools then run in your account on
that host while you supervise them from your usual Forge.

## Create the right workspace

From a pull request, Forge checks out the pull request branch. From an issue,
it starts from the repository's default branch and creates an issue branch. An
ad-hoc workspace starts from any tracked repository and uses the branch name
you choose.

The **New workspace** dialog also searches a selected Kata daemon. A Kata task
needs a repository mapping before Forge can create its worktree.

To create a workspace and launch an agent in one step, pick the agent from the
create menu. Creating the same item or ad-hoc branch again reopens the existing
workspace instead of making a duplicate.

The lightning icon beside the create menu lists your configured quick actions.
A quick action creates the workspace, launches its agent, and sends a preset
prompt once the agent is ready, so a routine task such as "rebase this pull
request" takes one click. Configure quick actions under **Settings → Quick
actions** (see [Configuration](../configuration.md#quick-actions)).

Inside a workspace, the same lightning icon appears in each pane's tab strip.
When a pane shows a running agent or chat, point at a quick action and click
the send icon at the end of its row to send the prompt to that agent instead of
launching a new one. A chat receives the prompt as a message, and a terminal
agent has it typed and submitted, regardless of which agent the quick action
names. Shells have no send icon.

### Choose a machine in a fleet

On a fleet hub, **New workspace** includes a **Run on** selector where you can
pick the hub or any spoke that currently allows workspace writes. Unavailable
machines stay in the list with the reason they cannot be selected. The machine
you choose owns the worktree, Git commands, agent processes, and tmux sessions.

The hub workspace list adds a node badge to every row so you can tell local and
remote workspaces apart. A spoke shows only its local workspaces and creates
new workspaces on itself.

The workspace buttons on pull requests and issues create work on the Forge UI
you opened. When you want a different execution machine, use the hub's **New
workspace** dialog. See [Federated fleet](../federated-fleet.md) for the full
ownership model and setup workflow.

## Use the workspace view

In the workspace sidebar, you can search, sort, and group workspaces. Put `!`
or `NOT` before a search word to hide matching workspaces. Each row shows the
repository, branch, linked item, changed-line counts, and current agent state
when lifecycle hooks report one.

When you select a local workspace, the main area shows the base shell in a tab
labeled **Workspace** next to a tab for each launched agent or shell session. A
remote fleet workspace has no **Workspace** tab and shows only tabs for its
launched agent or shell sessions.
The right-hand panel has buttons for:

- **Diff.** The workspace's changes compared with **HEAD** or the pushed
  **Branch**. Both are always available. The merge **Target** appears only when
  the workspace has a linked pull request.
- **PR** or **Issue.** The linked pull request or issue.
- **Kata.** The linked Kata task (local workspaces only, not remote fleet
  workspaces).
- **Reviews.** Roborev jobs for local pull request workspaces only. In a remote
  fleet workspace, the panel states that local reviews are unavailable.

You can launch another shell or agent from the workspace header. Sessions keep
running under tmux when you leave the page, reload the browser, or attach from
another terminal.

To continue an earlier conversation with an ACP agent, click the history button
on the agent's card in the launcher. The list shows the sessions the agent keeps
for the workspace's worktree, including ones started outside Forge, such as in
the agent's own CLI. Pick one to open it in a new chat with its earlier
messages. Sessions already open in a chat in the workspace are not listed. An
agent that cannot list and reload its sessions says so instead of showing a
list.

Forge enables application clipboard copying on its dedicated tmux server, with
no system-wide tmux change required. If you configure a custom shared tmux
server, enable `set-clipboard on` there to forward agent OSC 52 copies to the
browser. Copy from the active terminal with a mouse or keyboard gesture.

## Put sessions beside the work

Pull request, issue, and Activity detail views can host the same live
workspace. Move a session into its own pane when you want the terminal beside
the conversation or diff. You can reorder tabs, drag a tab to split the layout,
resize panes, hide a pane, or maximize the one that needs the screen.

Closing a pane changes the layout and does not stop the underlying tmux
session. To stop a process, use the session controls.

## Show agent state in the sidebar

Connect Claude Code and Codex to Forge with one command:

```sh
kenn-forge agent setup
```

This command installs lifecycle hooks for each client it finds and registers
[Forge MCP](../kenn-forge-mcp.md) when the daemon is running with MCP on. Run
`kenn-forge agent setup --check` to see what is connected, and add
`--agent NAME` to set up a single agent. After upgrading Forge, rerun setup so
existing hooks pick up changes, then review the updated Codex hooks in
`/hooks`.

With hooks installed, the sidebar can show when an agent is working, waiting
for approval, waiting for input, or done. Without fresh hook reports, Forge falls
back to tmux activity. To manage hooks without MCP, run
`kenn-forge agent-hook install`.

On Windows, ending a conversation with `/clear` or exit clears its badge even
if another program, such as a virus scanner, has the activity file open. If
Forge can't clear it, the SessionEnd hook fails and names the file.

## Continue an agent you started elsewhere

With hooks installed, an agent running in any terminal gets a workspace when its
Git worktree holds the head of an open pull request. The workspace uses that
worktree and shows the agent's conversation in its chat as it happens.

- In a Herdr pane, you can type in either
  place and the other shows it. `herdr` must be on the Forge daemon's `PATH`.
- In any other terminal, the chat is read-only until the agent exits.
- Claude and Codex conversations show while the agent runs. Other agents' chats
  stay empty until the agent exits and Forge loads the session.
- After the agent exits, your next prompt continues the same session in Forge.
  This needs a chat agent in Settings whose key starts with the agent's name,
  such as `claude-chat`.
- The worktree needs a remote for the repository the pull request targets. A
  checkout whose only remote is a fork gets no workspace.

Tool approvals stay in the agent's terminal while it runs. Deleting the workspace
keeps the worktree and branch and leaves the agent running.

## Attach outside the browser

Workspace and session controls expose tmux attach commands for a full local
terminal or a remote workspace reached through Fleet. Your browser and terminal
attach to the same durable session.

## Delete or recover a workspace

Deleting a workspace removes the Forge-managed worktree and its workspace
record. A dirty worktree blocks normal deletion so uncommitted work is not
discarded. Review the files first; force deletion becomes available only after
that warning.

To delete several workspaces at once, Shift- or Cmd/Ctrl-click them in the
list, right-click, and choose **Delete N workspaces...**. The dialog closes
when you confirm, and each row shows **Deleting workspace** until its delete
finishes. Bulk deletion never forces, so workspaces with uncommitted changes
stay. When it finishes, Forge names each workspace it could not delete and why.

If issue workspace creation finds the expected directory already present, Forge
can recover it after verifying that it belongs to the managed repository. A
mismatched repository or branch stays untouched and needs manual attention.

## Use workspaces on a phone

Open `/m/workspaces` for the phone layout, which uses workspace cards and shows
one terminal at a time. The session picker switches between the base shell and
running agents without stopping background sessions.

<figure class="workflow-shot workflow-shot--phone">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/mobile-workspace-session-light.svg" alt="Forge phone workspace with a Codex terminal and the input composer open in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/mobile-workspace-session-dark.svg" alt="Forge phone workspace with a Codex terminal and the input composer open in dark mode">
  <figcaption>The phone view keeps one terminal on screen, with session switching at the top and software-keyboard input at the bottom.</figcaption>
</figure>

The phone composer helps with software keyboards, while direct terminal input
still works with a hardware keyboard. Pull request and issue links return to
the same mobile workspace and selected session.
