# Documentation development

The public site build downloads its current workflow screenshots from the
orphan `docs-assets` branch. It does not launch the application or a browser.

Build the public site from those published assets:

```sh
make docs-build
```

The build writes the rendered site to `site/`. Both the rendered site and the
synced screenshot cache are ignored local artifacts. Verify the rendered site
in Chromium and WebKit separately:

```sh
make docs-check
```

The site is tiered: `site/` holds the static marketing pages copied from
`website/` (the pitch page at `/` and the Guide at `/guide/`), and the Zensical
build renders the docs under `site/docs/`. The build also copies `docs/llms.txt`
to `/llms.txt` and places each published page's raw markdown beside its
rendered directory (`/docs/index.md`, `/docs/<page>.md`) for machine readers, then
fails if any rendered page, markdown twin, or `llms.txt` entry is missing. New
pages must be added in four places: the file itself, the `zensical.toml` nav,
the allowlist in `scripts/build-docs.mjs`, and `docs/llms.txt`.

## Publishing

The Vercel project serves production deployments at `forge.kenn.io`. A
successful tagged release starts a default-branch workflow that verifies the
released commit belongs to `main` and still backs GitHub's latest release. It
builds that exact checkout on Vercel without changing the production alias,
then rechecks the latest release tag and commit before promoting it through
Vercel's project API.
The workflow checks again after promotion; if a newer release was published
during that interval, it dispatches a trusted workflow that resolves and
deploys the actual latest release. Promotion attempts are not placed in a
lossy GitHub concurrency group. No GitHub environment or human approval is
required. The project has no Vercel Git integration. Each remote build installs
Zensical, downloads the complete screenshot set, and publishes `site/`. It does
not install Go, build the application, or run Playwright.

To reproduce that remote build after installing its dependencies, run:

```sh
make docs-vercel-build
```

The Vercel install command targets Amazon Linux. On other development hosts,
use `make docs-build`; on-demand Vercel previews verify the exact Amazon Linux
install and static build path.

## Updating workflow screenshots

Screenshot generation is an authoring workflow, not part of deployment. It
requires Poppler's `pdftocairo`, the root Bun dependencies, and Playwright
Chromium. Generate a complete replacement set with:

```sh
make docs-screenshots
```

Set `OUTPUT_DIR=/tmp/kenn-forge-docs-assets` to write elsewhere. Review the
complete set, then replace the root contents of the orphan `docs-assets` branch
and push that branch. Record the resulting full commit SHA in
`scripts/docs-assets.ref` and land that change through normal review. Builds
only use that pinned commit and reject incomplete or unsafe SVG sets. Pushing
`docs-assets` alone does not change or deploy the site; run the normal preview
or production deployment command after the pin lands.

For a manual deployment, link a checkout to the project once from the
repository root:

```sh
vercel link
```

Start a preview build with:

```sh
make docs-deploy-staging
```

Start a production build with:

```sh
make docs-deploy
```

These targets send the current repository source to Vercel and use the same
install and build commands as release deployments. Run production deployments
from a tagged checkout so the site continues to describe the latest release.

If an automatic build or promotion fails after the GitHub release is
published, rerun the `Deploy documentation` workflow. The manual production
target remains the recovery path when Vercel or GitHub cannot complete the
automatic workflow.
