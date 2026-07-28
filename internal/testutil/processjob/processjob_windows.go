//go:build windows

package processjob

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	containOnce sync.Once
	containErr  error
	jobHandle   windows.Handle
)

// ContainCurrentProcessTree assigns the current process to a Windows Job
// Object whose descendants are terminated when this process exits. The job
// handle intentionally remains open for the lifetime of the process: closing
// it while the current process is still running would terminate the caller.
func ContainCurrentProcessTree() error {
	containOnce.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			containErr = fmt.Errorf("create process job: %w", err)
			return
		}

		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags =
			windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, err = windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		)
		if err != nil {
			_ = windows.CloseHandle(job)
			containErr = fmt.Errorf("configure process job: %w", err)
			return
		}
		if err := windows.AssignProcessToJobObject(
			job, windows.CurrentProcess(),
		); err != nil {
			_ = windows.CloseHandle(job)
			containErr = fmt.Errorf("assign current process to job: %w", err)
			return
		}
		jobHandle = job
	})
	return containErr
}
