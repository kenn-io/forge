//go:build windows

package agentactivity

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isReportLockError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
