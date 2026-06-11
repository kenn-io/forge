package messages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	SavedSearchesEnv = "MIDDLEMAN_MESSAGES_SAVED_SEARCHES_PATH"

	savedSearchesHeader = `# middleman messages saved searches - managed by middleman. Edit by hand
# if you prefer; middleman rewrites this file on every saved-search update.

`

	savedSearchesMax      = 50
	savedSearchesMaxName  = 200
	savedSearchesMaxQuery = 500
)

type SavedSearch struct {
	Name  string `toml:"name" json:"name"`
	Query string `toml:"query" json:"query"`
}

func SavedSearchesPath() (string, error) {
	if p := os.Getenv(SavedSearchesEnv); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("saved-searches path: %w", err)
	}
	return filepath.Join(home, ".config", "middleman", "messages-saved-searches.toml"), nil
}

func LoadSavedSearches() ([]SavedSearch, error) {
	path, err := SavedSearchesPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read saved-searches: %w", err)
	}
	var doc struct {
		Search []SavedSearch `toml:"search"`
	}
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return nil, fmt.Errorf("parse saved-searches: %w", err)
	}
	return doc.Search, nil
}

func SaveSavedSearches(list []SavedSearch) error {
	path, err := SavedSearchesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create saved-searches dir: %w", err)
	}
	body := savedSearchesHeader + renderSavedSearchesTOML(list)
	tmp, err := os.CreateTemp(filepath.Dir(path), "messages-saved-searches.toml.*.tmp")
	if err != nil {
		return fmt.Errorf("create saved-searches tmp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod saved-searches tmp: %w", err)
	}
	if _, err := tmp.WriteString(body); err != nil {
		cleanup()
		return fmt.Errorf("write saved-searches tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close saved-searches tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename saved-searches: %w", err)
	}
	return nil
}

func CanonicalizeSavedSearches(in []SavedSearch) []SavedSearch {
	out := make([]SavedSearch, 0, len(in))
	indexByName := make(map[string]int, len(in))
	for _, e := range in {
		query := strings.TrimSpace(e.Query)
		if query == "" {
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" {
			name = query
		}
		name = truncateRunes(name, savedSearchesMaxName)
		query = truncateRunes(query, savedSearchesMaxQuery)
		key := strings.ToLower(name)
		if idx, ok := indexByName[key]; ok {
			out[idx] = SavedSearch{Name: name, Query: query}
			indexByName[key] = idx
			continue
		}
		indexByName[key] = len(out)
		out = append(out, SavedSearch{Name: name, Query: query})
		if len(out) > savedSearchesMax {
			delete(indexByName, strings.ToLower(out[0].Name))
			out = out[1:]
			for k, v := range indexByName {
				indexByName[k] = v - 1
			}
		}
	}
	return out
}

func truncateRunes(s string, maxRunes int) string {
	if len(s) <= maxRunes {
		return s
	}
	runes := 0
	for i := range s {
		if runes == maxRunes {
			return s[:i]
		}
		runes++
	}
	return s
}

func SavedSearchesETag(list []SavedSearch) string {
	if list == nil {
		list = []SavedSearch{}
	}
	canon, err := json.Marshal(list)
	if err != nil {
		canon = []byte("[]")
	}
	sum := sha256.Sum256(canon)
	return `"sha256:` + hex.EncodeToString(sum[:]) + `"`
}

func renderSavedSearchesTOML(list []SavedSearch) string {
	var b strings.Builder
	for _, e := range list {
		b.WriteString("[[search]]\n")
		fmt.Fprintf(&b, "name = %s\n", tomlBasicString(e.Name))
		fmt.Fprintf(&b, "query = %s\n", tomlBasicString(e.Query))
		b.WriteByte('\n')
	}
	return b.String()
}

func tomlBasicString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
