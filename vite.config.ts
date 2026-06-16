import { defineConfig } from "vite-plus";

const rootVP = "node node_modules/vite-plus/bin/vp";
const frontendVP = "node ../node_modules/vite-plus/bin/vp";
const uiVP = "node ../../node_modules/vite-plus/bin/vp";
const packageVP = "node ../../node_modules/vite-plus/bin/vp";

export default defineConfig({
  run: {
    tasks: {
      "frontend-check": {
        command: [`${rootVP} run frontend-fmt`, `${rootVP} run frontend-lint`, `${rootVP} run svelte-check`],
        cache: false,
      },
      "frontend-fmt": {
        command: `${rootVP} fmt --check frontend packages/ui packages/github-app-ui --no-error-on-unmatched-pattern --threads=1`,
        cache: false,
      },
      "frontend-lint": {
        command: `${rootVP} lint frontend packages/ui packages/github-app-ui '!frontend/dist/**' '!packages/github-app-ui/dist/**' '!frontend/test-results/**' '!packages/github-app-ui/test-results/**' '!packages/ui/src/api/generated/**' '!packages/ui/src/api/roborev/generated/**' --no-error-on-unmatched-pattern --threads=1`,
        cache: false,
      },
      "frontend-package-check": {
        command: [`${rootVP} fmt --check frontend --no-error-on-unmatched-pattern --threads=1`, `${rootVP} run frontend-package-typecheck`],
        cache: false,
      },
      "frontend-package-typecheck": {
        command: [
          `${rootVP} lint frontend '!frontend/dist/**' '!frontend/test-results/**' --no-error-on-unmatched-pattern --threads=1`,
          `${rootVP} run svelte-check`,
        ],
        cache: false,
      },
      // svelte-check behaves poorly with concurrent processes. Keep one task
      // name and route package wrappers through it; cache hits keep repeated
      // package-local typechecks cheap without exposing per-package tasks.
      "svelte-check": {
        command: [
          `(cd frontend && ${frontendVP} exec -- svelte-check --tsconfig ./tsconfig.json --fail-on-warnings)`,
          `(cd packages/ui && ${uiVP} exec -- svelte-check --tsconfig ./tsconfig.json --fail-on-warnings)`,
          `(cd packages/github-app-ui && ${packageVP} exec -- svelte-check --tsconfig ./tsconfig.json --fail-on-warnings)`,
        ],
        input: [
          { auto: true },
          "!node_modules/.vite-temp/**",
          "!frontend/node_modules/.vite-temp/**",
          "!packages/ui/node_modules/.vite-temp/**",
          "!packages/github-app-ui/node_modules/.vite-temp/**",
          "!node_modules/.vite/task-cache/**",
          "!frontend/node_modules/.vite/task-cache/**",
          "!packages/ui/node_modules/.vite/task-cache/**",
          "!packages/github-app-ui/node_modules/.vite/task-cache/**",
        ],
      },
      "ui-package-check": {
        command: [`${rootVP} fmt --check packages/ui --no-error-on-unmatched-pattern --threads=1`, `${rootVP} run ui-package-typecheck`],
        cache: false,
      },
      "ui-package-typecheck": {
        command: [
          `${rootVP} lint packages/ui '!packages/ui/src/api/generated/**' '!packages/ui/src/api/roborev/generated/**' --no-error-on-unmatched-pattern --threads=1`,
          `${rootVP} run svelte-check`,
        ],
        cache: false,
      },
      "github-app-ui-package-check": {
        command: [
          `${rootVP} fmt --check packages/github-app-ui --no-error-on-unmatched-pattern --threads=1`,
          `${rootVP} run github-app-ui-package-typecheck`,
        ],
        cache: false,
      },
      "github-app-ui-package-typecheck": {
        command: [
          `${rootVP} lint packages/github-app-ui '!packages/github-app-ui/dist/**' --no-error-on-unmatched-pattern --threads=1`,
          `${rootVP} run svelte-check`,
        ],
        cache: false,
      },
    },
  },
  staged: {
    "*": `${rootVP} check --fix`,
  },
  fmt: {
    ignorePatterns: [
      "frontend/dist/**",
      "packages/github-app-ui/dist/**",
      "frontend/test-results/**",
      "packages/github-app-ui/test-results/**",
      "packages/ui/src/api/generated/**",
      "packages/ui/src/api/roborev/generated/**",
    ],
    printWidth: 120,
    sortImports: false,
  },
  lint: {
    ignorePatterns: [
      "frontend/scripts/**",
      "frontend/tests/**",
      "frontend/src/**/*.test.ts",
      "frontend/src/**/*.bench.test.ts",
      "packages/github-app-ui/dist/**",
      "packages/github-app-ui/test-results/**",
      "packages/ui/src/api/generated/**",
      "packages/ui/src/api/roborev/generated/**",
      "packages/ui/src/**/*.test.ts",
      "packages/ui/src/**/*.bench.test.ts",
    ],
    rules: {
      // Oxlint does not yet model Svelte template writes reliably. Watch the
      // embedded-framework RFC for the eventual fix path; the direct open
      // evidence is a bind:this false positive in a related rule.
      // https://github.com/oxc-project/oxc/discussions/21936
      // https://github.com/oxc-project/oxc/issues/19470
      // https://github.com/oxc-project/oxc/issues/15761
      "eslint/no-unassigned-vars": "off",
      // These type-aware rules are useful for cleanup work, but enabling them
      // during the Vite+ migration would turn existing non-buggy code into
      // unrelated churn that the previous frontend check never enforced.
      "typescript/no-base-to-string": "off",
      "typescript/no-duplicate-type-constituents": "off",
      "typescript/no-floating-promises": "off",
      "typescript/no-redundant-type-constituents": "off",
      // Keep the migration scoped to tool consolidation; these style rules
      // disagree with existing readable test and store code but do not affect
      // the consistency gains from moving checks under Vite+.
      "unicorn/no-useless-fallback-in-spread": "off",
      "unicorn/prefer-string-starts-ends-with": "off",
    },
    options: { typeAware: true, typeCheck: true },
  },
});
