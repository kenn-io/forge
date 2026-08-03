# Public Documentation Readiness Design

## Goal

Prepare Kenn Forge's public documentation for people encountering the product
for the first time. The finished site should explain what Kenn Forge does, get
a new user into the app, and support the main maintainer workflows without
requiring them to read internal design or implementation notes.

## Audience

The primary reader maintains repositories and is comfortable with a terminal,
but does not know Kenn Forge's architecture or configuration model. Advanced
operators still need accurate references for provider hosts, archives, fleet
operation, and troubleshooting.

## Scope

Audit and revise the complete public documentation surface:

- `README.md`
- `docs/index.md`
- `docs/quickstart.md`
- `docs/configuration.md`
- `docs/commands.md`
- `docs/workflows.md`
- `docs/workflows/issue-triager.md`
- `docs/workflows/code-reviewer.md`
- `docs/archive.md`
- `docs/federated-fleet.md`
- `docs/troubleshooting.md`
- the Zensical navigation, styles, screenshot cases, and screenshot guidance

ADRs, implementation plans, design specs, reports, and `context/` documents
remain internal. This work may update their links only when a public page would
otherwise point readers into internal material.

## Information Architecture

`README.md` becomes a short repository landing page. It explains the product,
links to releases and the documentation site, shows the shortest supported
start path, and sends detailed questions to the public guide. It must not
duplicate the configuration or workflow reference.

The documentation home answers three questions in order: what the product is,
how to start, and where to find a task-specific guide. Quick Start takes a new
user from installation through the first-run flow introduced by PR #816. It
leads with release binaries for Linux, macOS, and Windows, then offers a source
build for contributors and unreleased builds.

The remaining pages keep their current subject boundaries:

- Workflows describes routine use of the product.
- Configuration documents durable settings and credentials.
- Commands documents the supported CLI surface.
- Archive and Fleet cover their advanced operating models.
- Troubleshooting starts with observable symptoms and gives direct recovery
  steps.

Navigation labels should use the terms shown in the UI. Cross-links should
move readers to the next useful task without repeating the destination page.

## Content Rules

Public prose uses the `unslop` crisp preset. Pages lead with the action or fact,
use active voice, and keep paragraphs short. Feature lists describe user
outcomes instead of implementation details. Commands, paths, provider names,
defaults, limits, and security boundaries must remain exact.

Every public page goes through the two-pass `unslop` workflow:

1. Extract facts and scan the existing prose for banned patterns.
2. Rewrite for clarity, then validate fact preservation, banned patterns,
   readability, and change size.

Large reductions are acceptable when they remove duplicated or internal prose.
The final review still checks every removed fact against the product and the
remaining reference pages.

## Visual Documentation

Screenshots remain generated build artifacts. The repository tracks the
Playwright cases and page references, not the generated SVG files.

The screenshot suite uses the isolated seeded backend. It must never capture a
developer's running app or private data. Each capture records a stable, useful
state after sync and loading indicators settle. Light and dark variants must
render from the same case.

The visual inventory should cover distinct user questions, not every mode:

- first-run provider readiness or repository selection in Quick Start
- the main Activity or pull-request workflow on the docs home or workflow
  overview
- issue triage
- code review

Existing issue-triage and code-review captures must be regenerated from the
current UI. Add or replace the first-run and overview captures only when they
improve the surrounding instructions. Each image needs specific alt text and a
caption that explains what the reader should notice.

## Implementation Boundaries

This work updates public documentation, its build pipeline, and isolated
screenshot fixtures. It does not change production onboarding behavior or add
new product features. If a documentation audit finds a product defect, record
it separately instead of widening this PR.

The branch is stacked directly above onboarding PR #816. Its pull request base
must remain `t3code/design-onboarding-mockups` so reviewers see only the public
documentation layer.

## Verification

Verification covers source text, generated artifacts, and the rendered site:

- Run every public Markdown file through the `unslop` validation scripts.
- Run the docs build and its screenshot Playwright cases against the isolated
  seeded backend.
- Run the docs build script tests.
- Inspect every generated SVG in light and dark mode for current UI, stable
  data, clipped content, loading states, and private information.
- Inspect the rendered Zensical site at desktop and phone widths.
- Check navigation, internal links, code blocks, headings, image paths, alt
  text, and theme switching in rendered output.
- Confirm generated screenshot assets remain untracked.

The work is complete when a new user can install Kenn Forge, finish onboarding,
understand the main workflows, and reach accurate advanced references without
encountering stale UI or internal project language.
