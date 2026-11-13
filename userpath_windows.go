//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// addToUserPath puts dir on the signed-in user's PATH (HKCU\Environment), so the program runs
// as a plain command in any new terminal. Returns whether PATH changed. Skipped as SYSTEM,
// whose PATH is not the user's.
func addToUserPath(dir string) (bool, error) {
	if inServiceSession() {
		return false, nil
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	cur, typ, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return false, err
	}
	if pathListHas(cur, dir) {
		return false, nil
	}
	next := dir
	if strings.TrimSpace(cur) != "" {
		next = strings.TrimRight(cur, ";") + ";" + dir
	}
	if typ == registry.SZ {
		err = k.SetStringValue("Path", next)
	} else {
		err = k.SetExpandStringValue("Path", next)
	}
	if err != nil {
		return false, err
	}
	broadcastEnvChange()
	return true, nil
}

// pathListHas tells whether a ;-separated PATH already holds dir (case and trailing \ ignored).
func pathListHas(list, dir string) bool {
	want := strings.ToLower(strings.TrimRight(filepath.Clean(dir), `\`))
	for _, e := range strings.Split(list, ";") {
		e = strings.TrimSpace(strings.Trim(strings.TrimSpace(e), `"`))
		if e != "" && strings.ToLower(strings.TrimRight(filepath.Clean(e), `\`)) == want {
			return true
		}
	}
	return false
}

// broadcastEnvChange tells Explorer and other programs that the environment changed, so new
// terminals see the PATH without signing out.
func broadcastEnvChange() {
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
	env, _ := windows.UTF16PtrFromString("Environment")
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	var res uintptr
	proc.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 2000, uintptr(unsafe.Pointer(&res)))
}
