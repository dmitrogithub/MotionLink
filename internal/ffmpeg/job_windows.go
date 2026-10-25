//go:build windows

package ffmpeg

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	jobOnce   sync.Once
	jobHandle windows.Handle
	jobError  error
)

// bindToHostLifetime puts FFmpeg into a job object that Windows terminates
// when the host process exits for any reason, including a crash or a forced
// kill. Without it an orphaned FFmpeg keeps capturing the desktop and keeps
// sending RTP into the port the next host instance listens on.
func bindToHostLifetime(process *os.Process) error {
	jobOnce.Do(func() {
		jobHandle, jobError = windows.CreateJobObject(nil, nil)
		if jobError != nil {
			return
		}
		limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, jobError = windows.SetInformationJobObject(
			jobHandle,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)),
			uint32(unsafe.Sizeof(limits)),
		)
	})
	if jobError != nil {
		return fmt.Errorf("create job object: %w", jobError)
	}

	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(process.Pid))
	if err != nil {
		return fmt.Errorf("open FFmpeg process: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(jobHandle, handle); err != nil {
		return fmt.Errorf("assign FFmpeg to job object: %w", err)
	}
	return nil
}
