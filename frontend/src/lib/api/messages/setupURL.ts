/**
 * Shared message source setup URL validation. Mirrors the Go server's
 * `validateConfigureURL` so the dialog, the mock backend, and the
 * server all reject the same inputs.
 *
 * WHATWG `new URL()` rescues several inputs that Go's `url.Parse`
 * rejects with an empty host:
 *   - `https:///foo`  (three slashes; empty authority)
 *   - `https:/foo`    (one slash after colon; treated as path)
 *   - `https:foo`     (no slashes; opaque)
 * Callers must reject these BEFORE constructing `new URL(...)` so the
 * dialog, mock backend, and server agree on what is configurable.
 */

/**
 * hasEmptyAuthority detects http/https inputs whose authority component
 * (the `//host` portion) is missing or empty. We rule out anything that
 * isn't a proper `scheme://host...` form: missing slashes after the colon
 * normalize unpredictably in WHATWG URL parsing, and the Go server
 * rejects all of these.
 */
export function hasEmptyAuthority(raw: string): boolean {
  // Require exactly "://" after the scheme and at least one non-slash
  // character before the next "/" (the host portion). Anything else -
  // "scheme:" without slashes, single-slash "scheme:/foo", triple-slash
  // "scheme:///foo" - is rejected here.
  return !/^[^:]+:\/\/[^/]/.test(raw);
}

/**
 * isLoopbackHostname mirrors the server's loopback check for plaintext
 * http URLs: only localhost and loopback IP literals are accepted.
 * `hostname` is WHATWG `URL.hostname` (IPv6 keeps its brackets).
 */
export function isLoopbackHostname(hostname: string): boolean {
  const h = hostname.replace(/\.$/, "").toLowerCase();
  if (h === "localhost" || h === "[::1]" || h === "::1") return true;
  return /^127(\.\d{1,3}){3}$/.test(h);
}
