import { Effect, Exit, Fiber, Option, Schema } from "effect";
import { AppBridge, PostMessageTransport } from "@modelcontextprotocol/ext-apps/app-bridge";
import type { AppRuntime } from "../../app/runtime.js";
import { InvalidExternalPayload, TransientTransportError } from "../../api/effect-errors.js";
import { executeGeneratedRequest } from "../../api/generated-api.js";
import { callMcpAppTool, getMcpAppResource } from "../../api/generated/mcp-apps/mcp-apps.js";
import type { ChatContent } from "./chat-types.js";

const AppDescriptor = Schema.Struct({ title: Schema.String, html: Schema.String });
export type AppDescriptor = typeof AppDescriptor.Type;
const TaggedAppDescriptor = Schema.Struct({
  kind: Schema.Literal("kenn-forge://apps/generated"),
  title: Schema.String,
  html: Schema.String,
});
export function decodeAppDescriptor(content: ChatContent): AppDescriptor | undefined {
  if (content.type !== "text") return;
  return Option.getOrUndefined(Schema.decodeUnknownOption(Schema.fromJsonString(TaggedAppDescriptor))(content.text));
}

// data: isolates the proxy from Forge. The inner frame also omits
// allow-same-origin so generated code cannot alter or execute in the proxy.
export const sandboxProxyURL = `data:text/html;base64,${btoa(String.raw`<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-src 'self' about:; connect-src 'none'; form-action 'none'; base-uri 'none'"><style>html,body,iframe{margin:0;width:100%;height:100%;border:0}iframe{display:block}</style></head><body><script>
let view;
addEventListener('message', event => {
  if (event.source === parent) {
    if (event.data?.method === 'ui/notifications/sandbox-resource-ready' && !view) {
      view = document.createElement('iframe');
      view.setAttribute('sandbox', 'allow-scripts');
      view.srcdoc = '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; script-src \'unsafe-inline\'; style-src \'unsafe-inline\'; img-src data:; connect-src \'none\'; form-action \'none\'; base-uri \'none\'">' + event.data.params.html;
      document.body.append(view);
    } else if (view) view.contentWindow.postMessage(event.data, '*');
  } else if (view && event.source === view.contentWindow) parent.postMessage(event.data, '*');
});
parent.postMessage({jsonrpc:'2.0',method:'ui/notifications/sandbox-proxy-ready',params:{}}, '*');
</script></body></html>`)}`;

const sdkCall = <A>(operation: string, call: () => Promise<A>) =>
  Effect.tryPromise({
    try: call,
    catch: (cause) => TransientTransportError.make({ operation, cause }),
  });

export const hostApp = Effect.fn("McpApp.host")(function* (
  frame: HTMLIFrameElement,
  descriptor: AppDescriptor,
  runtime: AppRuntime,
  update: (status: string, height?: number) => void,
  requestLink: (url: string) => void,
) {
  const scope = yield* Effect.scope;
  const { html } = yield* executeGeneratedRequest("load MCP App", (signal) => getMcpAppResource({ signal }));
  const bridge = yield* Effect.acquireRelease(
    Effect.sync(
      () =>
        new AppBridge(
          null,
          { name: "kenn-forge", version: "0.1.0" },
          { serverTools: {}, openLinks: {} },
          {
            hostContext: {
              displayMode: "inline",
              theme: document.documentElement.classList.contains("dark") ? "dark" : "light",
            },
          },
        ),
    ),
    (bridge) =>
      sdkCall("close MCP App", () => bridge.teardownResource({}, { timeout: 250 })).pipe(
        Effect.ignore,
        Effect.andThen(sdkCall("close MCP App transport", () => bridge.close()).pipe(Effect.ignore)),
      ),
  );
  // SDK handlers require Promises. Each request is still a child of the mounted
  // widget scope, so unmount interrupts in-flight cache reads.
  const run = <A, E>(program: Effect.Effect<A, E>) =>
    runtime
      .runCommand(program.pipe(Effect.forkIn(scope), Effect.flatMap(Fiber.join)), {
        operation: "MCP App request",
        safeContext: {},
        onFailure: () => update("Widget request failed. Try refreshing the component."),
      })
      .exit.then((exit) => {
        if (Exit.isSuccess(exit)) return exit.value;
        throw new Error("MCP App request failed or was interrupted");
      });
  bridge.oncalltool = (params) =>
    run(
      executeGeneratedRequest("MCP App cache read", (signal) =>
        callMcpAppTool({ name: params.name, arguments: params.arguments ?? {} }, { signal }),
      ).pipe(
        Effect.flatMap((value) =>
          Schema.decodeUnknownEffect(
            Schema.Struct({
              content: Schema.Array(Schema.Struct({ type: Schema.Literal("text"), text: Schema.String })),
              structuredContent: Schema.optional(Schema.Record(Schema.String, Schema.Unknown)),
              isError: Schema.optional(Schema.Boolean),
              _meta: Schema.optional(Schema.Record(Schema.String, Schema.Unknown)),
            }),
          )(value).pipe(
            Effect.mapError((cause) => InvalidExternalPayload.make({ operation: "decode MCP App tool result", cause })),
          ),
        ),
        Effect.map((result) => ({ ...result, content: [...result.content] })),
      ),
    );
  bridge.onopenlink = async ({ url }) => {
    const parsed = new URL(url);
    if (!["http:", "https:"].includes(parsed.protocol)) return { isError: true };
    requestLink(parsed.href);
    return {};
  };
  bridge.onsizechange = ({ height }) => update("Ready", Math.max(120, Math.min(height ?? 320, 900)));
  bridge.onsandboxready = () => {
    void run(
      sdkCall("load app sandbox", () =>
        bridge.sendSandboxResourceReady({ html, sandbox: "allow-scripts allow-same-origin" }),
      ),
    ).catch(() => {});
  };
  bridge.oninitialized = () => {
    void run(
      sdkCall("initialize MCP App", async () => {
        await bridge.sendToolInput({ arguments: descriptor });
        await bridge.sendToolResult({ content: [], structuredContent: descriptor });
        update("Ready");
      }),
    ).catch(() => {});
  };
  const target = frame.contentWindow;
  if (!target) return;
  yield* sdkCall("connect MCP App", () => bridge.connect(new PostMessageTransport(target, target)));
  frame.src = sandboxProxyURL;
  return yield* Effect.never;
});
