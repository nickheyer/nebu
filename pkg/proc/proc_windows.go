//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GetExitCodeProcess reports this for a process that is still running
const stillActive = 259

// Starts the child as its own console process group so it can be interrupted
func attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// Puts the process in a job object, so its whole tree ends with the job and with us
func adopt(pid int) *Tree {
	t := &Tree{pid: pid}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return t
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return t
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return t
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return t
	}
	t.job = uintptr(job)
	return t
}

func interruptTree(t *Tree) { Interrupt(t.pid) }

func killTree(t *Tree) {
	if t.job != 0 {
		windows.TerminateJobObject(windows.Handle(t.job), 1)
		return
	}
	Kill(t.pid)
}

func closeTree(t *Tree) {
	if t.job != 0 {
		windows.CloseHandle(windows.Handle(t.job))
		t.job = 0
	}
}

// Sends the group a break, what a console process treats as an interrupt
func Interrupt(pid int) {
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid)); err != nil {
		// A process on another console cannot be signalled, so it gets a close request instead
		exec.Command("taskkill", "/PID", strconv.Itoa(pid)).Run()
	}
}

// Ends a foreign process with everything it spawned
func Kill(pid int) {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
			windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
}

// Reports whether a process with pid exists
func Exists(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// Returns the command line of pid as the system recorded it
func Cmdline(pid int) (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_Process -Filter 'ProcessId="+strconv.Itoa(pid)+"').CommandLine").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
