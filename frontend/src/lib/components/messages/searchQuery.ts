export function addFilterToQuery(query: string, token: string): string {
  const trimmedToken = token.trim();
  const tokens = query.trim().split(/\s+/).filter(Boolean);
  if (!trimmedToken) return tokens.join(" ");

  const tokenKey = trimmedToken.toLowerCase();
  if (tokens.some((item) => item.toLowerCase() === tokenKey)) {
    return tokens.join(" ");
  }
  return [...tokens, trimmedToken].join(" ");
}
