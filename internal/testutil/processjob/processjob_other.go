//go:build !windows

package processjob

// ContainCurrentProcessTree is a no-op outside Windows.
func ContainCurrentProcessTree() error {
	return nil
}
