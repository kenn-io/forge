package landedwork

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// Message is one commit's message, split at its trailer block.
type Message struct {
	Commit string
	// Text is the message without its trailer block, cut at a UTF-8 boundary
	// to the caller's byte bound. Truncated reports that it was cut.
	Text      string
	Truncated bool
	// Trailers are the lines of the trailer block, in order, read from the
	// whole message even when Text was cut.
	Trailers []Trailer
}

// Trailer is one "Key: value" line of a trailer block. A continuation line
// is joined to the value with a single space.
type Trailer struct{ Key, Value string }

// Messages reads each commit's message from the repository at path, in the
// order given. A message's trailer block is its last paragraph when every
// line in it is a trailer or the continuation of one; a message of one
// paragraph has none. Object reads charge limits.InputBytes. maxText bounds
// each message's Text, not its trailers.
func Messages(ctx context.Context, path string, commits []string, maxText int, limits Limits) (_ []Message, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if maxText <= 0 || limits.InputBytes <= 0 {
		return nil, errors.New("message limits must be positive")
	}
	for _, id := range commits {
		if !objectID(id) {
			return nil, errors.New("invalid commit object ID")
		}
	}
	v, err := openView(ctx, path, &meter{limits: limits})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, v.close()) }()
	messages := make([]Message, 0, len(commits))
	for _, id := range commits {
		object, readErr := v.readObject(ctx, id, "commit", true)
		if readErr != nil {
			return nil, readErr
		}
		m := Message{Commit: id}
		m.Text, m.Trailers = splitTrailers(commitMessage(object))
		m.Text, m.Truncated = cutText(m.Text, maxText)
		messages = append(messages, m)
	}
	return messages, nil
}

// commitMessage returns the message of a raw commit object, which follows
// the first blank line after its headers.
func commitMessage(object []byte) string {
	_, message, found := bytes.Cut(object, []byte("\n\n"))
	if !found {
		return ""
	}
	return string(message)
}

func splitTrailers(message string) (string, []Trailer) {
	lines := trimBlankLines(strings.Split(message, "\n"))
	start := len(lines)
	for start > 0 && strings.TrimSpace(lines[start-1]) != "" {
		start--
	}
	text := strings.Join(lines, "\n")
	if start == 0 {
		return text, nil
	}
	var trailers []Trailer
	for _, line := range lines[start:] {
		if line[0] == ' ' || line[0] == '\t' {
			if len(trailers) == 0 {
				return text, nil
			}
			last := &trailers[len(trailers)-1]
			last.Value += " " + strings.TrimSpace(line)
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found || !trailerKey(key) {
			return text, nil
		}
		trailers = append(trailers, Trailer{Key: key, Value: strings.TrimSpace(value)})
	}
	return strings.Join(trimBlankLines(lines[:start]), "\n"), trailers
}

// trimBlankLines drops trailing lines that hold only whitespace.
func trimBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// trailerKey reports whether key is a trailer token: letters, digits and
// hyphens, starting with a letter or digit.
func trailerKey(key string) bool {
	if key == "" || key[0] == '-' {
		return false
	}
	for _, r := range key {
		letter := 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
		if !letter && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func cutText(text string, maxBytes int) (string, bool) {
	if len(text) <= maxBytes {
		return text, false
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n], true
}

// ParseIdentity reads a trailer value of the form "Name <email>", as
// Co-authored-by and Signed-off-by carry. Both parts must be present.
func ParseIdentity(value string) (name, email string, ok bool) {
	value = strings.TrimSpace(value)
	open := strings.LastIndexByte(value, '<')
	if open < 0 || !strings.HasSuffix(value, ">") {
		return "", "", false
	}
	name, email = strings.TrimSpace(value[:open]), value[open+1:len(value)-1]
	if name == "" || email == "" || strings.ContainsAny(email, "<> \t") || !strings.Contains(email, "@") {
		return "", "", false
	}
	return name, email, true
}
