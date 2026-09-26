import type { Plugin } from "vite";

export function frontendApiClient(): Plugin;
export function generateClient(frontendDir?: string): Promise<void>;
