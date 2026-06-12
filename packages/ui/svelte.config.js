// svelte-check 4.5+ resolves Svelte config by walking up from each file and
// stops at the first svelte.config.js or vite.config.*. Without this file it
// reaches the repo-root vite.config.ts (the vite-plus task config, which has
// no Svelte plugin) and errors on every component. These components compile
// through frontend/vite.config.ts with default svelte() options, so the
// matching config here is empty.
export default {};
