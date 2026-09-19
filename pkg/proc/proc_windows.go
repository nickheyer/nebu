//go:build windows

package proc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GetExitCodeProcess reports this for a process that is still running
const stillActive = 259

// Names the process a helper invocation breaks into, set only on that invocation
const breakEnv = "NEBU_CONSOLE_BREAK"

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	user32               = windows.NewLazySystemDLL("user32.dll")
	procAllocConsole     = kernel32.NewProc("AllocConsole")
	procAttachConsole    = kernel32.NewProc("AttachConsole")
	procFreeConsole      = kernel32.NewProc("FreeConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procSetCtrlHandler   = kernel32.NewProc("SetConsoleCtrlHandler")
	procShowWindow       = user32.NewProc("ShowWindow")
	// Held for the life of the process so the job, and every descendant in it, ends with us
	ownJob windows.Handle
)

// Creates a hidden console and inherited job for child processes. To interrupt adopted processes on
// another console, starts a helper with breakEnv that attaches and sends the break.
func Init() {
	if pid, err := strconv.Atoi(os.Getenv(breakEnv)); err == nil {
		procFreeConsole.Call()
		if r, _, _ := procAttachConsole.Call(uintptr(pid)); r != 0 {
			procSetCtrlHandler.Call(0, 1)
			windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
		}
		os.Exit(0)
	}
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd == 0 {
		if r, _, _ := procAllocConsole.Call(); r != 0 {
			if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
				procShowWindow.Call(hwnd, 0)
			}
		}
	}
	if job := killOnCloseJob(); job != 0 {
		if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
			windows.CloseHandle(job)
			return
		}
		ownJob = job
	}
}

// Creates a job whose processes end when its last handle closes, zero when the OS refuses
func killOnCloseJob() windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0
	}
	return job
}

// Starts the child as its own console process group so it can be interrupted
func attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// Creates a job for the process tree, nested in the daemon job when available.
func adopt(pid int) *Tree {
	t := &Tree{pid: pid}
	job := killOnCloseJob()
	if job == 0 {
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

func interruptTree(t *Tree) { interrupt(t.pid) }

func killTree(t *Tree) {
	if t.job != 0 {
		windows.TerminateJobObject(windows.Handle(t.job), 1)
		return
	}
	kill(t.pid)
}

func closeTree(t *Tree) {
	if t.job != 0 {
		windows.CloseHandle(windows.Handle(t.job))
		t.job = 0
	}
}

// Sends the group a break, what a console process treats as an interrupt
func interrupt(pid int) {
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid)); err == nil {
		return
	}
	// A process on another console is reached by a copy of us attached to that console
	exe, err := os.Executable()
	if err != nil {
		return
	}
	helper := exec.Command(exe)
	helper.Env = append(os.Environ(), breakEnv+"="+strconv.Itoa(pid))
	helper.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	helper.Run()
}

// Ends a foreign process with everything it spawned
func kill(pid int) {
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
func cmdline(pid int) (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_Process -Filter 'ProcessId="+strconv.Itoa(pid)+"').CommandLine").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
