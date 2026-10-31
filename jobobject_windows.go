//go:build windows

package main

// A Job Object with KILL_ON_JOB_CLOSE holds the children serve must not outlive (cloudflared).
// serve keeps the job handle open for its whole life; when serve exits for any reason, Windows
// closes the handle and kills everything in the job. Children start suspended and are resumed
// only after they are in the job, so none can escape.

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var (
	procCreateJobObjectW         = modKernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = modKernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = modKernel32.NewProc("AssignProcessToJobObject")
	procNtResumeProcess          = syscall.NewLazyDLL("ntdll.dll").NewProc("NtResumeProcess")

	childJobOnce sync.Once
	childJob     syscall.Handle
	childJobErr  error
)

type jobBasicLimits struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobIOCounters struct {
	ReadOperationCount, WriteOperationCount, OtherOperationCount uint64
	ReadTransferCount, WriteTransferCount, OtherTransferCount    uint64
}

type jobExtendedLimits struct {
	Basic                 jobBasicLimits
	IO                    jobIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
	createSuspended                   = 0x4
)

func killOnCloseJob() (syscall.Handle, error) {
	childJobOnce.Do(func() {
		h, _, err := procCreateJobObjectW.Call(0, 0)
		if h == 0 {
			childJobErr = fmt.Errorf("CreateJobObject: %v", err)
			return
		}
		var info jobExtendedLimits
		info.Basic.LimitFlags = jobObjectLimitKillOnJobClose
		r, _, err := procSetInformationJobObject.Call(h, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
		if r == 0 {
			syscall.CloseHandle(syscall.Handle(h))
			childJobErr = fmt.Errorf("SetInformationJobObject: %v", err)
			return
		}
		childJob = syscall.Handle(h) // never closed: closing happens when serve exits
	})
	return childJob, childJobErr
}

// prepareChildInJob makes the child start suspended (putChildInJob resumes it).
func prepareChildInJob(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createSuspended
}

// putChildInJob adds the started (suspended) child to the kill-on-close job, then resumes it.
// It always resumes, even when the job can't be used.
func putChildInJob(pid int) error {
	const access = 0x0100 | 0x0001 | 0x0800 | 0x1000 // SET_QUOTA | TERMINATE | SUSPEND_RESUME | QUERY_LIMITED
	h, _, err := procOpenProcess.Call(access, 0, uintptr(pid))
	if h == 0 {
		return fmt.Errorf("OpenProcess %d: %v", pid, err)
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	var jerr error
	if job, e := killOnCloseJob(); e != nil {
		jerr = e
	} else if r, _, e := procAssignProcessToJobObject.Call(uintptr(job), h); r == 0 {
		jerr = fmt.Errorf("AssignProcessToJobObject: %v", e)
	}
	if st, _, _ := procNtResumeProcess.Call(h); st != 0 {
		return fmt.Errorf("NtResumeProcess: status 0x%x", st)
	}
	return jerr
}
