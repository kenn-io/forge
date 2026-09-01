# Read and edit local Docs

Docs turns registered Markdown folders into a local reading and editing
workspace. The files stay in those folders. Kenn Forge does not copy them into
its database.

<figure class="workflow-shot">
  <img class="workflow-shot__image workflow-shot__image--light" src="../assets/generated/docs-workspace-light.svg" alt="Forge Docs workspace with a Markdown file, folder tree, and document outline in light mode">
  <img class="workflow-shot__image workflow-shot__image--dark" src="../assets/generated/docs-workspace-dark.svg" alt="Forge Docs workspace with a Markdown file, folder tree, and document outline in dark mode">
  <figcaption>Docs keeps the folder tree, rendered Markdown, and document outline together.</figcaption>
</figure>

## Register a folder

Enable Docs and add a folder:

```toml
[modes]
docs = true
```

```sh
kenn-forge docs add-folder --name Notes ~/notes
```

The folder switcher appears after you register more than one folder. Each
folder opens its `README.md` or `index.md` when one exists.

## Find and read a document

Use the tree filter to narrow files by name. Docs reads Markdown files and
supported local images while ignoring content outside the registered folder.
Links between Markdown files and wiki-style links open inside the Docs view.

The outline follows headings in the selected document. A link with a heading
fragment opens the document at that section, so copied Docs URLs can point to a
specific passage.

Open the command palette and type a query to search filenames and document
content across every registered folder. Results open the matching document in
Docs. The filter above the file tree is quicker when you already know part of a
filename in the current folder.

## Edit files

Choose **Edit** to change the current Markdown file. You can also create,
rename, and delete Markdown files from the file controls. Save writes the file
back to disk.

Docs never overwrites an existing destination during create or rename. If a
file changed on disk after you opened it, reload the current document before
saving your edit.

## Pull and publish Git changes

Git-backed folders show **Pull** and **Publish** controls. Pull accepts only a
fast-forward update and refuses to overwrite tracked changes. Ignored,
untracked files follow Git's usual pull behavior: Git may replace one if the
incoming commit starts tracking the same path.

Publish shows the Markdown changes before it commits and pushes them. It stages
Markdown changes only and includes Markdown files that are already fully
staged. Staged non-Markdown files, partially staged Markdown files, conflicts,
or a branch without an upstream block publication until you resolve them with
Git.

If a push fails after the commit succeeds, the local commit remains in the
folder. Fix the remote or upstream problem, then run `git push` in that folder.
Publish cannot retry because it has already committed the Markdown changes.

## Open Kata references

Bind a folder to one Kata daemon when its task references belong there:

```sh
kenn-forge docs add-folder --id project --daemon kata-main ~/project-docs
```

Recognized references can open the task in Kata. Docs does not show inline Kata
task detail, and Kata remains responsible for edits to the task.

Docs is currently a desktop-first view. It has no dedicated `/m` route.
