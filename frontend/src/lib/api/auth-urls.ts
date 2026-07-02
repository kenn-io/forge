// window.__BASE_PATH__ always ends in "/", so concatenation yields a
// single slash. logoutHref hits the server-side cookie-expiry endpoint.
export function logoutHref(basePath: string): string {
  return `${basePath}auth/logout`;
}
