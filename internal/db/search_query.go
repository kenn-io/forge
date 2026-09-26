package db

import (
	"slices"
	"strings"
)

// SearchQuery is free-text list search split into terms. Every Include term
// must match and no Exclude term may match. A term is negated by a leading
// "!" ("!bot", `!"needs review"`) or by a preceding standalone uppercase
// "NOT" or "!" token. Quoting keeps whitespace and the literal words "NOT" or
// "!", so `"NOT"` searches for the word itself.
type SearchQuery struct {
	Include []string
	Exclude []string
}

// ParseSearchQuery splits search into included and excluded terms. A trailing
// negation operator with no term after it is ignored so partially typed
// queries such as "fix NOT" still behave like "fix".
func ParseSearchQuery(search string) SearchQuery {
	var q SearchQuery
	negateNext := false
	for _, tok := range searchTokens(search) {
		if !tok.quoted && (tok.text == "NOT" && !tok.bang || tok.text == "" && tok.bang) {
			negateNext = true
			continue
		}
		if tok.text == "" {
			continue
		}
		if tok.bang || negateNext {
			q.Exclude = append(q.Exclude, tok.text)
		} else {
			q.Include = append(q.Include, tok.text)
		}
		negateNext = false
	}
	return q
}

type searchToken struct {
	text   string
	quoted bool
	bang   bool
}

// searchTokens splits on unquoted whitespace. A quote opens a phrase only at
// the start of a token (after an optional "!"), so apostrophes inside words
// stay literal.
func searchTokens(search string) []searchToken {
	var tokens []searchToken
	var b strings.Builder
	var tok searchToken
	var quote rune

	flush := func() {
		tok.text = b.String()
		if tok.text != "" || tok.quoted || tok.bang {
			tokens = append(tokens, tok)
		}
		b.Reset()
		tok = searchToken{}
	}

	for _, r := range search {
		atStart := b.Len() == 0 && !tok.quoted
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
		case r == '!' && atStart && !tok.bang:
			tok.bang = true
		case (r == '"' || r == '\'') && atStart:
			quote = r
			tok.quoted = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// Empty reports whether the query has no terms at all.
func (q SearchQuery) Empty() bool {
	return len(q.Include) == 0 && len(q.Exclude) == 0
}

// Matches reports whether the fields satisfy the query, comparing each term
// case-insensitively as a substring of any single field.
func (q SearchQuery) Matches(fields ...string) bool {
	lowered := make([]string, len(fields))
	for i, field := range fields {
		lowered[i] = strings.ToLower(field)
	}
	containsTerm := func(term string) bool {
		term = strings.ToLower(term)
		return slices.ContainsFunc(lowered, func(field string) bool {
			return strings.Contains(field, term)
		})
	}
	for _, term := range q.Include {
		if !containsTerm(term) {
			return false
		}
	}
	return !slices.ContainsFunc(q.Exclude, containsTerm)
}

// sqlCondition ANDs termCondition once per term, wrapping excluded terms in
// NOT COALESCE(..., 0) so NULL columns never hide rows from a negated search.
// termCondition must contain argsPerTerm placeholders, each bound to the
// term's LIKE pattern built by pattern.
func (q SearchQuery) sqlCondition(termCondition string, argsPerTerm int, pattern func(string) string) (string, []any) {
	if q.Empty() {
		return "", nil
	}
	conds := make([]string, 0, len(q.Include)+len(q.Exclude))
	args := make([]any, 0, (len(q.Include)+len(q.Exclude))*argsPerTerm)
	appendArgs := func(term string) {
		like := pattern(term)
		for range argsPerTerm {
			args = append(args, like)
		}
	}
	for _, term := range q.Include {
		conds = append(conds, "("+termCondition+")")
		appendArgs(term)
	}
	for _, term := range q.Exclude {
		conds = append(conds, "NOT COALESCE(("+termCondition+"), 0)")
		appendArgs(term)
	}
	return "(" + strings.Join(conds, " AND ") + ")", args
}
