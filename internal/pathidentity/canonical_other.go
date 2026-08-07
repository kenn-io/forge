//go:build !darwin && !linux && !windows

package pathidentity

// CanonicalExisting returns path unchanged on platforms without a handle API.
func CanonicalExisting(path string) (string, error) {
	return path, nil
}
