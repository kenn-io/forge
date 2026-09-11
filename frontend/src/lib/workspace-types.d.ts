/**
 * Build environment declarations shared by frontend modules.
 */

// import.meta.env shape for source modules that do not import Vite directly,
// so the bundled `vite/client` types aren't resolvable from this tsconfig.
// Declare the subset we actually read at runtime. `DEV` is the only field
// utils/ci-buckets-warn.ts inspects today; broaden as needed.
interface ImportMetaEnv {
  readonly DEV?: boolean;
  readonly MODE?: string;
  readonly PROD?: boolean;
  readonly SSR?: boolean;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
