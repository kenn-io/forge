# Documentation development

Build and verify the public site, including the generated workflow screenshots:

```sh
make docs-build
```

The build writes the rendered site to `site/`. Both the rendered site and its
generated screenshots are ignored local artifacts.

## Publishing

The Vercel project serves production deployments at `forge.kenn.io`. A
successful tagged release starts a default-branch workflow that verifies the
released commit belongs to `main` and still backs GitHub's latest release. It
builds that exact checkout on Vercel without changing the production alias,
then rechecks the latest release tag and commit in a serialized promotion job.
The workflow checks again after promotion; if a newer release was published
during that interval, its successful deployment reconciles the production
alias. No GitHub environment or human approval is required. The project has no
Vercel Git integration. Each remote build installs the screenshot runtime,
generates the workflow screenshots from the seeded e2e server, verifies the
rendered site, and publishes `site/`.

To reproduce that remote build after installing its dependencies, run:

```sh
make docs-vercel-build
```

The Vercel install command targets Amazon Linux. On other development hosts,
use `make docs-build`; on-demand Vercel previews verify the exact Amazon Linux
install and build path.

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
