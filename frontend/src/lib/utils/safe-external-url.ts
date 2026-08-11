export function isSafeExternalHTTPURL(rawURL: string): boolean {
  if (!/^https?:\/\/[^/\\]/i.test(rawURL)) return false;
  try {
    const target = new URL(rawURL);
    return (
      (target.protocol === "http:" || target.protocol === "https:") &&
      target.host !== "" &&
      target.username === "" &&
      target.password === ""
    );
  } catch {
    return false;
  }
}
