export function shouldRetryFleetDiffWatch(status: number): boolean {
  if (status === 501) return false;
  return status === 408 || status === 409 || status === 425 || status === 429 || status >= 500;
}
