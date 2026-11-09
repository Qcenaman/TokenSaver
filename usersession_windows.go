package main

import (
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// inServiceSession: running as the boot service (LocalSystem, session 0), where there is no
// desktop, no Git sign-in of the user's and no window to read.
func inServiceSession() bool {
	h, _ := os.UserHomeDir()
	return isSystemProfile(h)
}

// runInUserSession starts exe with args as the signed-in user, on their desktop, and waits up
// to wait for it to end (0 = don't wait). Only the service account can do this; it lowers the
// program's rights from the service's to the user's.
func runInUserSession(exe string, args []string, dir string, wait time.Duration) (int, error) {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF {
		return -1, errors.New("nobody is signed in on this PC")
	}
	var tok windows.Token
	if err := windows.WTSQueryUserToken(session, &tok); err != nil {
		return -1, fmt.Errorf("no signed-in user to run as: %v", err)
	}
	defer tok.Close()
	var primary windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return -1, err
	}
	defer primary.Close()
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, primary, false); err != nil {
		return -1, err
	}
	defer windows.DestroyEnvironmentBlock(env)
	cl, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{exe}, args...)))
	if err != nil {
		return -1, err
	}
	desk, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si := windows.StartupInfo{Desktop: desk, Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	si.Cb = uint32(unsafe.Sizeof(si))
	var dirp *uint16
	if dir != "" {
		dirp, _ = windows.UTF16PtrFromString(dir)
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcessAsUser(primary, nil, cl, nil, nil, false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, env, dirp, &si, &pi); err != nil {
		return -1, fmt.Errorf("could not start as the signed-in user: %v", err)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	if wait <= 0 {
		return 0, nil
	}
	if ev, _ := windows.WaitForSingleObject(pi.Process, uint32(wait/time.Millisecond)); ev != windows.WAIT_OBJECT_0 {
		return -1, errors.New("still running after " + wait.String())
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return -1, err
	}
	return int(code), nil
}
