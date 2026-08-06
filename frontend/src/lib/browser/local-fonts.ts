export interface LocalFontData {
  family: string;
  fullName: string;
  postscriptName: string;
  style: string;
}

declare global {
  interface Window {
    queryLocalFonts?: () => Promise<LocalFontData[]>;
  }
}

export function supportsLocalFonts(): boolean {
  return typeof window !== "undefined" && typeof window.queryLocalFonts === "function";
}

export function queryLocalFonts(): Promise<LocalFontData[]> {
  const query = window.queryLocalFonts;
  if (query === undefined) {
    return Promise.reject(new DOMException("Local font access is unavailable", "NotSupportedError"));
  }
  return query.call(window);
}
