/// <reference types="vite/client" />

declare var __kenn_forgeForceSyntaxHighlight: boolean | undefined;

declare module "*.svelte?retry" {
  const component: typeof import("./lib/features/docs/DocsFeature.svelte").default;
  export default component;
}

declare module "*.svelte?retry2" {
  const component: typeof import("./lib/features/docs/DocsFeature.svelte").default;
  export default component;
}

declare module "@xterm/addon-ligatures/lib/addon-ligatures.mjs" {
  export { LigaturesAddon } from "@xterm/addon-ligatures";
}

interface Window {
  __KENN_FORGE_SERVICE_MODE__?: boolean;
  __BASE_PATH__?: string;
  __KENN_FORGE_DEV_API_URL__?: string;
  __KENN_FORGE_FORCE_MOBILE_ROUTES__?: boolean;
  __kenn_forge_active_worktree_key?: string;
  __kenn_forge_event_source_counts?: () => {
    created: number;
    closed: number;
  };
  __kenn_forge_kata_graph_debug?: {
    snapshot: () => {
      events: Array<{
        id: number;
        at: number;
        kind: string;
        detail?: Record<string, unknown> | undefined;
      }>;
      latestGraph?:
        | {
            sourceUID: string;
            selectedUID: string | null;
            hideDone: boolean;
            contextDepth: string;
            depthLimit: string;
            layoutMode: string;
            layoutDirection: string;
            layoutReady: boolean;
            nodeIds: string[];
            edges: Array<{ id: string; source: string; target: string; kind: string | null; isDepthContext: boolean }>;
            nodePositions: Array<{ id: string; x: number; y: number }>;
            disabledNodeIds: string[];
            missingRefKeys: string[];
            nodeCount: number;
            edgeCount: number;
            layoutEdgeCount: number;
            layoutBounds: { width: number; height: number; aspectRatio: number };
          }
        | undefined;
      store?:
        | {
            queueKeys: string[];
            graphLoadActive: boolean;
            issueRefreshActive: boolean;
            pendingSelectionUID: string | null;
            selectedIssueUID: string | null;
            cachedTaskCount: number;
          }
        | undefined;
    };
    reset: () => void;
  };
}
