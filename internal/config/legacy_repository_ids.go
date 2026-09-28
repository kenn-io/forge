package config

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// upgradeRepositoryIDs rewrites platform_repo_id values that older releases
// saved as text into the provider's integer repository ID. Decimal text
// converts directly. GitHub node IDs encode the integer ID, so they decode
// without a provider call. A repository entry whose text cannot be decoded
// loses only its pin; a preset member without a decodable ID is dropped,
// because presets require a verified ID. It reports false when the document
// has no text IDs, so current configs decode untouched.
func upgradeRepositoryIDs(data string) (string, bool, error) {
	if !strings.Contains(data, "platform_repo_id") {
		return data, false, nil
	}
	var doc map[string]any
	if _, err := toml.Decode(data, &doc); err != nil {
		return data, false, err
	}
	changed := false
	if repos, ok := doc["repos"].([]map[string]any); ok {
		for _, repo := range repos {
			raw, ok := repo["platform_repo_id"].(string)
			if !ok {
				continue
			}
			changed = true
			if id, ok := decodeLegacyRepositoryID(raw); ok {
				repo["platform_repo_id"] = id
				continue
			}
			slog.Warn("config: dropping undecodable repository ID pin",
				"owner", repo["owner"], "name", repo["name"])
			delete(repo, "platform_repo_id")
		}
	}
	if presets, ok := doc["repo_presets"].([]map[string]any); ok {
		for _, preset := range presets {
			members, ok := preset["repos"].([]map[string]any)
			if !ok {
				continue
			}
			kept := members[:0]
			for _, member := range members {
				raw, ok := member["platform_repo_id"].(string)
				if !ok {
					kept = append(kept, member)
					continue
				}
				changed = true
				if id, ok := decodeLegacyRepositoryID(raw); ok {
					member["platform_repo_id"] = id
					kept = append(kept, member)
					continue
				}
				slog.Warn("config: dropping preset member with undecodable repository ID",
					"preset", preset["name"], "repo_path", member["repo_path"])
			}
			preset["repos"] = kept
		}
	}
	if !changed {
		return data, false, nil
	}
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(doc); err != nil {
		return data, false, err
	}
	return out.String(), true, nil
}

// legacyGitHubNodeID matches the original GitHub global node ID payload,
// "<type name length>:Repository<integer ID>".
var legacyGitHubNodeID = regexp.MustCompile(`^[0-9]+:Repository([0-9]+)$`)

func decodeLegacyRepositoryID(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return id, id > 0
	}
	if payload, ok := strings.CutPrefix(raw, "R_"); ok {
		return decodeGitHubNextNodeID(payload)
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
		if err != nil {
			return 0, false
		}
	}
	match := legacyGitHubNodeID.FindSubmatch(decoded)
	if match == nil {
		return 0, false
	}
	id, err := strconv.ParseInt(string(match[1]), 10, 64)
	return id, err == nil && id > 0
}

// decodeGitHubNextNodeID reads GitHub's current repository node ID: URL-safe
// base64 of the MessagePack array [0, integer ID].
func decodeGitHubNextNodeID(payload string) (int64, bool) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(payload, "="))
	if err != nil || len(b) < 3 || b[0] != 0x92 || b[1] != 0x00 {
		return 0, false
	}
	var id uint64
	switch body := b[2:]; {
	case body[0] < 0x80 && len(body) == 1:
		id = uint64(body[0])
	case body[0] == 0xcc && len(body) == 2:
		id = uint64(body[1])
	case body[0] == 0xcd && len(body) == 3:
		id = uint64(binary.BigEndian.Uint16(body[1:]))
	case body[0] == 0xce && len(body) == 5:
		id = uint64(binary.BigEndian.Uint32(body[1:]))
	case body[0] == 0xcf && len(body) == 9:
		id = binary.BigEndian.Uint64(body[1:])
	default:
		return 0, false
	}
	if id == 0 || id > 1<<63-1 {
		return 0, false
	}
	return int64(id), true
}
