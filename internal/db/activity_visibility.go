package db

import "strings"

var botLoginSuffixes = [...]string{"[bot]", "-bot", "bot"}

// IsBotLogin is the provider-neutral Activity bot heuristic. It intentionally
// mirrors the provider login suffixes that Activity has historically used.
func IsBotLogin(login string) bool {
	normalized := strings.ToLower(strings.TrimSpace(login))
	if normalized == "" {
		return false
	}
	for _, suffix := range botLoginSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func activityNotBotCondition(column string) string {
	conditions := make([]string, 0, len(botLoginSuffixes))
	for _, suffix := range botLoginSuffixes {
		conditions = append(conditions, "LOWER(TRIM("+column+")) NOT LIKE '%"+suffix+"'")
	}
	return "(" + strings.Join(conditions, " AND ") + ")"
}
