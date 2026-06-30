let authenticated = $state(true);

export function isAuthenticated(): boolean {
  return authenticated;
}

export function setAuthenticated(): void {
  authenticated = true;
}

export function setUnauthenticated(): void {
  authenticated = false;
}
