// Free-text list search, mirroring internal/db/search_query.go so client-side
// filters (workspaces) and server-side filters (pulls, issues, activity) read a
// query the same way.

export interface SearchQuery {
  include: string[];
  exclude: string[];
}

interface SearchToken {
  text: string;
  quoted: boolean;
  bang: boolean;
}

// Splits on unquoted whitespace. A quote opens a phrase only at the start of a
// token (after an optional "!"), so apostrophes inside words stay literal.
function searchTokens(search: string): SearchToken[] {
  const tokens: SearchToken[] = [];
  let text = "";
  let quoted = false;
  let bang = false;
  let quote = "";

  const flush = () => {
    if (text !== "" || quoted || bang) tokens.push({ text, quoted, bang });
    text = "";
    quoted = false;
    bang = false;
  };

  for (const ch of search) {
    const atStart = text === "" && !quoted;
    if (quote !== "") {
      if (ch === quote) quote = "";
      else text += ch;
    } else if (ch === "!" && atStart && !bang) {
      bang = true;
    } else if ((ch === '"' || ch === "'") && atStart) {
      quote = ch;
      quoted = true;
    } else if (/\s/.test(ch)) {
      flush();
    } else {
      text += ch;
    }
  }
  flush();
  return tokens;
}

// A term is negated by a leading "!" or a preceding standalone uppercase "NOT"
// or "!" token. Quoted "NOT" is a literal word, and a trailing operator with no
// term is ignored so partially typed queries keep matching.
export function parseSearchQuery(search: string): SearchQuery {
  const query: SearchQuery = { include: [], exclude: [] };
  let negateNext = false;
  for (const tok of searchTokens(search)) {
    if (!tok.quoted && ((tok.text === "NOT" && !tok.bang) || (tok.text === "" && tok.bang))) {
      negateNext = true;
      continue;
    }
    if (tok.text === "") continue;
    (tok.bang || negateNext ? query.exclude : query.include).push(tok.text.toLowerCase());
    negateNext = false;
  }
  return query;
}

export function isEmptySearchQuery(query: SearchQuery): boolean {
  return query.include.length === 0 && query.exclude.length === 0;
}

// Every included term must be a case-insensitive substring of some field, and
// no excluded term may be.
export function matchesSearchQuery(query: SearchQuery, fields: ReadonlyArray<string | null | undefined>): boolean {
  const lowered = fields.map((field) => field?.toLowerCase() ?? "");
  const contains = (term: string) => lowered.some((field) => field.includes(term));
  return query.include.every(contains) && !query.exclude.some(contains);
}
