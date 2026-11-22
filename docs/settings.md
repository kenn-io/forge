# Change settings in the app

To change everyday Kenn Forge behavior, open the gear in the app header. The
menu groups related settings and includes a search box to jump straight to a
category.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="assets/generated/settings-overview-light.svg" alt="Forge Settings with the category menu and repository controls visible in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="assets/generated/settings-overview-dark.svg" alt="Forge Settings with the category menu and repository controls visible in dark mode">
  <figcaption>Settings puts repository, workflow, workspace, and navigation choices in one searchable menu.</figcaption>
</figure>

## What you can change

| Category | What it controls |
| --- | --- |
| Repositories | Import repositories, refresh tracking globs, choose local clones, and hide repositories from the app. |
| Sync | Pause automatic background sync with airplane mode. Relay updates, item visits, and manual Sync remain available. |
| Pull requests | Choose whether Forge may merge a pull request from the middle of a detected stack. |
| Detail views and Activity | Set the initial timeline size and the defaults used when Activity opens. |
| Workspaces | Choose workspace creation behavior, optional Roborev initialization for managed clones, the default right sidebar, ACP fonts, and terminal appearance and retention. |
| Kata mappings | Override the repository matched to a Kata project. |
| Workspace agents | Enable agents and edit the command and arguments used to launch each one. |
| Fleet federation | Connect remote Forge hosts and control which sessions they share. |
| Visible modes | Choose which pages appear in the app header. |
| Appearance | Choose the color theme and contrast, and show the desktop PR sidebar as two-line rows or as the table Activity uses, with Repo, Author, Item, and When columns and each pull request's status at the end of its Item cell. Sidebars 560px or narrower hide When and shorten Repo and Author to leave room for titles. Narrower sidebars, focus lists, and phone lists use two-line rows. Switches under **Shown in the PR table** hide the repository, author, labels, CI counts, review state, stack position, star, or time from the table. These choices apply only to the browser you set them in. |

Each form saves from its own panel, and you can switch between categories
without losing unfinished edits. Repository actions such as add, refresh, and
remove run when you confirm them.

## Pause automatic sync

To pause automatic background sync after any running update finishes, turn on
**Sync → Airplane mode**. Relay updates, opening an item, and manual **Sync**
still work while it is on. To jump back to the setting later, click
**airplane mode** in the status bar.

Airplane mode defaults to off, and Forge remembers your setting across
restarts. It applies to the Forge instance you are viewing. On a fleet spoke,
it pauses local background refresh, and the hub keeps its own setting. For the
configuration-file option, see [Airplane mode](configuration.md#airplane-mode).

## ACP fonts

To set a font family and size for all ACP harnesses, open **Workspaces → ACP**.
The font must be installed on the machine running your browser. If you leave
the family blank, Forge uses the app's default fonts. Terminal appearance has
separate settings.

Forge applies your changes to open ACP chats and saves them in `config.toml`:

```toml
[acp]
font_family = '"MesloLGS NF", monospace'
font_size = 16
```

## When to edit the configuration file

Settings does not store provider credentials. For credentials and for options
without an app control, such as the Roborev endpoint, tmux command, and Docs
folders, edit the configuration file as described in
[Configuration](configuration.md).
