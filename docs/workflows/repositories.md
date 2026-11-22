# Browse repository source

Open **Repos** to inspect the repositories Kenn Forge tracks. Each card
summarizes open work and recent repository state. Select **View repo** on a
card, or run **View repository source** from the command palette when a pull
request, issue, activity item, or workspace provides the repository context.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/repository-source-light.svg" alt="Forge repository source browser showing a Markdown file, file tree, and history in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/repository-source-dark.svg" alt="Forge repository source browser showing a Markdown file, file tree, and history in dark mode">
  <figcaption>The source browser keeps the ref, file tree, selected file, and file history in one view.</figcaption>
</figure>

## Pick a ref and file

Use the ref picker to switch between branches and tags, and use the path filter
to narrow the file tree without changing the selected ref.

Select a file to read its source. Markdown files offer **Preview** and
**Source** modes, headings in a preview can be linked directly, and supported
images render inside Markdown previews.

The history rail lists commits that changed the selected file. Open any commit
to inspect its SHA, author, date, subject, and commit message without losing
the current repository and path.

## Share the exact view

The browser URL records the provider host, repository, ref, file path, and
preview mode. Copying the URL preserves that state, and your browser's Back and
Forward buttons return to earlier refs and files.

Branch and tag names can move. Forge resolves the selected ref to a commit
while loading the page so the tree, file, and history belong to the same
revision.

## Refresh source

The source browser reads from its own local clone rather than a workspace
worktree, so uncommitted workspace changes do not appear here.

Forge creates the clone on first use and refreshes it automatically on the
configured sync interval, separately from repository sync. If a newly pushed
branch or tag has not appeared yet, wait for the next source refresh and reopen
the ref picker.
