# Embeds

Kenn Forge embed routes are intended to run in an isolated browser context, such
as an iframe, WebView, or host-owned panel that loads one Kenn Forge document per
surface. They are not designed for multiple Kenn Forge instances mounted into the
same `window`.

The embed shell is intentionally smaller than the standalone app shell. It
initializes theme handling, the workspace bridge, the shared API provider, and
the single requested workspace surface. It does not run standalone startup work:
settings hydration, sync polling, pull/issue preloads, event-stream connection,
container/sidebar setup, or global keyboard shortcuts.

Hosts communicate with embeds through the `window.__kenn_forge_*` bridge on that
isolated browsing context. Because the bridge and browser history are
document-global, callers that need more than one embed at a time should allocate
one iframe/WebView per embed instance rather than sharing a single document.
