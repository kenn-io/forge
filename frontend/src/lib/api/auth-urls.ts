// window.__BASE_PATH__ always ends in "/", so concatenation yields a
// single slash. loginHref drives the existing ?auth_token= cookie
// bootstrap; logoutHref hits the server-side cookie-expiry endpoint.
export function loginHref(basePath: string, token: string): string {
  return `${basePath}?auth_token=${encodeURIComponent(token)}`;
}

export function logoutHref(basePath: string): string {
  return `${basePath}auth/logout`;
}
