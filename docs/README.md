# Documentation development

Build and verify the public site, including the generated workflow screenshots:

```sh
make docs-build
```

The build writes the rendered site to `site/`. Both the rendered site and its
generated screenshots are ignored local artifacts.

## Publishing

The Vercel project serves production deployments at `forge.kenn.io`. Link a
checkout to that project once from the repository root:

```sh
vercel link
```

Build, verify, and create a preview deployment with:

```sh
make docs-deploy-staging
```

Publish the same locally verified build path to production with:

```sh
make docs-deploy
```

These targets package `site/` as Vercel prebuilt output, so Vercel receives the
exact pages and screenshots that passed the local docs checks.
