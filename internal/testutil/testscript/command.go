// Package testscript runs shell fixtures without executing newly written files.
package testscript

// Command invokes sh so parallel forks cannot cause ETXTBSY on the script.
func Command(path string, args ...string) []string {
	return append([]string{"sh", path}, args...)
}
