# Pull Request Workflow

Use this document before pushing, opening a pull request, or changing pull
request metadata, comments, or review threads.

- Treat this as a public repository. Before pushing or opening a pull request,
  run the scrub-private-data skill and remove unnecessary internal project
  names, hostnames, credentials, runner topology, and infrastructure details
  from the diff, commits, and pull request metadata.
- Do not watch or poll pull request GitHub Actions checks unless the user asks,
  or the work is running through the `$kenn:refine-pr` skill.
- Same-repository pull requests use main-branch reusable workflows on
  organization-managed ephemeral self-hosted runners. Fork pull requests use
  GitHub-hosted runners even for organization members; trust follows the head
  repository, and external fork runs also require explicit approval.
- Never delete, minimize, hide, or resolve pull request comments, review
  comments, review threads, or CI/review-bot comments unless the user explicitly
  asks for that exact action. Leave stale or contradicted comments in place and
  explain the current evidence in a reply or report.
- Keep pull request descriptions concise. A bulleted summary of user-visible
  changes is sufficient; omit test plans, implementation details, checklists,
  and marketing language.
- For visible UI changes, use the `capture-playwright` skill before opening the
  pull request and attach the screenshot or short video with `gh image` so the
  description can include the resulting artifact links.
