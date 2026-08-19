# Change settings in the app

Open the gear in the app header to change everyday kenn-forge behavior. The
menu groups related settings and includes a search box for jumping straight to
a category.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="assets/generated/settings-overview-light.svg" alt="kenn-forge Settings with the category menu and repository controls visible in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="assets/generated/settings-overview-dark.svg" alt="kenn-forge Settings with the category menu and repository controls visible in dark mode">
  <figcaption>Settings puts repository, workflow, workspace, and navigation choices in one searchable menu.</figcaption>
</figure>

## What you can change

| Category | What it controls |
| --- | --- |
| Repositories | Import repositories, refresh tracking globs, choose local clones, and hide repositories from the app. |
| Pull requests | Choose whether kenn-forge may merge a pull request from the middle of a detected stack. |
| Detail views and Activity | Set the initial timeline size and the defaults used when Activity opens. |
| Workspaces and Terminal | Choose workspace creation behavior, the default right sidebar, and terminal appearance. |
| Kata mappings | Override the repository matched to a Kata project. |
| Workspace agents | Enable agents and edit the command and arguments used to launch each one. |
| Fleet federation | Connect remote kenn-forge hosts and control which sessions they share. |
| Visible modes | Choose which pages appear in the app header. |

Forms save from the panel where you make the change. You can move between
categories without discarding an unfinished form. Repository actions such as
add, refresh, and remove run when you confirm the action.

## When to edit the configuration file

Settings does not store provider credentials. Use
[Configuration](configuration.md) for credentials and for options that do not
have an app control, including the Roborev endpoint, tmux command, and Docs
folders.
