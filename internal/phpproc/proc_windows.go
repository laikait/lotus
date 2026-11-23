//go:build windows

package phpproc

import (
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// procAttr keeps a console window from opening for each process.
func procAttr() *syscall.SysProcAttr {
	const createNoWindow = 0x08000000
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// The processes are put in a job object that kills them when the last handle to
// the job closes, which happens when the server process ends however it ends.
// Without it, a server killed from the Task Manager would leave its php-cgi
// processes running and holding their ports.
var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObject    = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJob  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJob = kernel32.NewProc("AssignProcessToJobObject")
	jobOnce                sync.Once
	job                    syscall.Handle
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
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

type ioCounters struct{ a, b, c, d, e, f uint64 }

type jobExtendedLimits struct {
	Basic                 jobBasicLimits
	IO                    ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

func createJob() {
	h, _, _ := procCreateJobObject.Call(0, 0)
	if h == 0 {
		return
	}
	var info jobExtendedLimits
	info.Basic.LimitFlags = jobObjectLimitKillOnJobClose
	r, _, _ := procSetInformationJob.Call(h, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if r == 0 {
		syscall.CloseHandle(syscall.Handle(h))
		return
	}
	job = syscall.Handle(h)
}

// confine puts the process in the kill-on-close job. It is best effort: if it
// fails the process still runs, it just won't die with the server.
func confine(p *os.Process) {
	jobOnce.Do(createJob)
	if job == 0 || p == nil {
		return
	}
	h, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(p.Pid))
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	procAssignProcessToJob.Call(uintptr(job), uintptr(h))
}
