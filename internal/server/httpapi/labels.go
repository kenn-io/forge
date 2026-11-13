package httpapi

import "strings"

// LabelNames drops blank names from a labels or exclude_labels query parameter.
func LabelNames(names []string) []string {
	var out []string
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
