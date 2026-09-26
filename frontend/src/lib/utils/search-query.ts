// Free-text list search, mirroring internal/db/search_query.go so client-side
// filters (workspaces) and server-side filters (pulls, issues, activity) read a
// query the same way.

export interface SearchQuery {
  include: string[];
  exclude: string[];
}

// A half-open [start, end) range of UTF-16 offsets into the raw query.
export interface SearchQueryRange {
  start: number;
  end: number;
}

interface SearchToken extends SearchQueryRange {
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
  let start = 0;
  let offset = 0;

  const flush = (end: number) => {
    if (text !== "" || quoted || bang) tokens.push({ text, quoted, bang, start, end });
    text = "";
    quoted = false;
    bang = false;
  };

  for (const ch of search) {
    const atStart = text === "" && !quoted;
    if (atStart && !bang && quote === "") start = offset;
    if (quote !== "") {
      if (ch === quote) quote = "";
      else text += ch;
    } else if (ch === "!" && atStart && !bang) {
      bang = true;
    } else if ((ch === '"' || ch === "'") && atStart) {
      quote = ch;
      quoted = true;
    } else if (/\s/.test(ch)) {
      flush(offset);
    } else {
      text += ch;
    }
    offset += ch.length;
  }
  flush(offset);
  return tokens;
}

interface AnalyzedSearchQuery {
  query: SearchQuery;
  operators: SearchQueryRange[];
}

// A term is negated by a leading "!" or a preceding standalone uppercase "NOT"
// or "!" token. Quoted "NOT" is a literal word, and a trailing operator with no
// term is ignored so partially typed queries keep matching. Operator ranges come
// from the same pass so highlighting cannot disagree with filtering.
function analyzeSearchQuery(search: string): AnalyzedSearchQuery {
  const query: SearchQuery = { include: [], exclude: [] };
  const operators: SearchQueryRange[] = [];
  let negateNext = false;
  for (const tok of searchTokens(search)) {
    if (!tok.quoted && tok.text === "NOT" && !tok.bang) {
      operators.push({ start: tok.start, end: tok.end });
      negateNext = true;
      continue;
    }
    if (!tok.quoted && tok.text === "" && tok.bang) {
      operators.push({ start: tok.start, end: tok.start + 1 });
      negateNext = true;
      continue;
    }
    if (tok.text === "") continue;
    if (tok.bang) operators.push({ start: tok.start, end: tok.start + 1 });
    (tok.bang || negateNext ? query.exclude : query.include).push(tok.text.toLowerCase());
    negateNext = false;
  }
  return { query, operators };
}

export function parseSearchQuery(search: string): SearchQuery {
  return analyzeSearchQuery(search).query;
}

// Ranges of the raw query that act as negation operators, in order.
export function searchQueryOperators(search: string): SearchQueryRange[] {
  return analyzeSearchQuery(search).operators;
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
